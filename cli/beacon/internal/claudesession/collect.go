package claudesession

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionwindow"
)

const StateVersion = 1

type Cursor struct {
	LastLine      int   `json:"last_line"`
	SizeBytes     int64 `json:"size_bytes,omitempty"`
	ModTimeUnixMS int64 `json:"mtime_unix_ms,omitempty"`
	Started       bool  `json:"started,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Files   map[string]*Cursor `json:"files"`
}

type CollectOptions struct {
	ProjectsDir string
	StatePath   string
	Write       bool
	LogPath     string
	UserMode    bool
	Print       bool
	Out         io.Writer
	// RotateBytes is the size at which this sweep rotates the runtime log; zero means the size
	// every Beacon writer uses. The archive count is not configurable here: the hooks and the
	// collector prune archives past the shared count on their next rotation, and readers only
	// look that far, so a sweep that kept more would lose them anyway.
	RotateBytes int64
	// Since, when set, limits the sweep to sessions modified at or after it, newest first. Older
	// sessions are not read and their cursors do not move, so a later sync still collects them.
	// The install-time backfill sets it; an ordinary sync leaves it zero.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
}

type Summary struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	MalformedLines  int `json:"malformed_lines"`
	PartialSessions int `json:"partial_sessions"`
	// EventsRetained is how many of the events this sweep wrote are still in the runtime log or
	// its archives when it finishes. Events the writer suppressed as duplicates are in neither.
	EventsRetained int `json:"events_retained"`
	// EventsRotatedOut is how many events this sweep wrote that another writer's rotations
	// discarded before it finished. The sweep never rotates out its own output.
	EventsRotatedOut int `json:"events_rotated_out"`
	// RetentionLimited is set when the sweep stopped because writing more would have rotated its
	// own output out of the log. SessionsPending is how many sessions still have records to read:
	// the one the sweep stopped inside (also counted in SessionsChanged when it wrote part of it)
	// and every later one with new records. Their cursors were left where they were.
	RetentionLimited bool `json:"retention_limited"`
	SessionsPending  int  `json:"sessions_pending"`
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "claude.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Claude",
	New:   func() *State { return &State{Version: StateVersion, Files: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Files != nil },
	Stamp: func(s *State) { s.Version = StateVersion },
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

func (s *State) cursor(path string) *Cursor {
	if s.Files == nil {
		s.Files = map[string]*Cursor{}
	}
	c := s.Files[path]
	if c == nil {
		c = &Cursor{}
		s.Files[path] = c
	}
	return c
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	store, err := NewStore(opts.ProjectsDir)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	refs = sessionwindow.Recent(refs, opts.Since, func(r SessionRef) int64 { return r.ModTimeUnixMS })
	summary.Sessions = len(refs)
	if len(refs) == 0 {
		return summary, nil
	}
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Claude collector state: %w", saveErr)
		}
	}()

	sink := opts.sink()
	sink.GuardRetention()
	var errs []error
	responses := responseLedger{}
	for i, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, sink, responses, &summary)
		if errors.Is(collectErr, writer.ErrRetentionWindowFull) {
			// Not a failed session: this one and the rest are simply left for the next sweep,
			// with cursors that have not moved past anything that was not written.
			if changed {
				summary.SessionsChanged++
			}
			summary.RetentionLimited = true
			summary.SessionsPending = 1 + countChanged(refs[i+1:], state)
			errs = append(errs, fmt.Errorf("stopped after %d events with %d Claude session(s) pending: %w",
				summary.EventsEmitted, summary.SessionsPending, collectErr))
			break
		}
		if errors.Is(collectErr, writer.ErrBudgetSpent) {
			// Not a failed session: this one and the rest are left for a later sync, with cursors
			// that have not moved past anything that was not written.
			if changed {
				summary.SessionsChanged++
			}
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Claude session %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	var rotatedOut error
	summary.EventsRetained, summary.EventsRotatedOut, rotatedOut = sink.Retention()
	if rotatedOut != nil {
		errs = append(errs, rotatedOut)
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

// countChanged is how many of refs have records the cursor has not reached, which is what a
// sweep that stopped before them leaves for the next one.
func countChanged(refs []SessionRef, state *State) int {
	n := 0
	for _, ref := range refs {
		c := state.Files[ref.Path]
		if c == nil || ref.SizeBytes != c.SizeBytes || ref.ModTimeUnixMS != c.ModTimeUnixMS {
			n++
		}
	}
	return n
}

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, sink sessionsync.Sink, responses responseLedger, summary *Summary) (bool, error) {
	cursor := state.cursor(ref.Path)
	if ref.SizeBytes < cursor.SizeBytes {
		cursor.LastLine = 0
		cursor.Started = false
	}
	if ref.SizeBytes == cursor.SizeBytes && ref.ModTimeUnixMS == cursor.ModTimeUnixMS {
		return false, nil
	}
	records, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if stats.PartialTail {
		summary.PartialSessions++
	}
	if err != nil {
		return false, err
	}
	mapped := MapSession(ref, records, MapOptions{MinLine: cursor.LastLine, SkipSessionStarted: cursor.Started, responses: responses})
	if len(mapped) == 0 {
		advanceCursor(cursor, ref, stats.Lines)
		return false, nil
	}
	for i, item := range mapped {
		if err := sink.Emit(item.Event); err != nil {
			advanceCursorPartial(cursor, mapped, i)
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	advanceCursor(cursor, ref, stats.Lines)
	return true, nil
}

func advanceCursor(cursor *Cursor, ref SessionRef, lines int) {
	cursor.LastLine = lines
	cursor.SizeBytes = ref.SizeBytes
	cursor.ModTimeUnixMS = ref.ModTimeUnixMS
}

// advanceCursorPartial moves the cursor past source lines whose mapped events
// were all emitted, so the next sweep retries only from the source line that
// failed. failedIdx is the index into mapped of the event whose emit returned
// an error. It never moves the cursor to or past the RetryFrom line of an event that was not
// written, so the source lines that event accounts for are read again.
func advanceCursorPartial(cursor *Cursor, mapped []MappedEvent, failedIdx int) {
	var lastCompleteLine int
	found := false
	for i := range mapped {
		if i == failedIdx {
			break
		}
		if i+1 < len(mapped) && mapped[i+1].SourceLine != mapped[i].SourceLine {
			lastCompleteLine = mapped[i].SourceLine
			found = true
		}
	}
	if !found {
		return
	}
	for _, item := range mapped[failedIdx:] {
		if item.RetryFrom > 0 && lastCompleteLine >= item.RetryFrom {
			lastCompleteLine = item.RetryFrom - 1
		}
	}
	if lastCompleteLine < cursor.LastLine {
		return
	}
	cursor.LastLine = lastCompleteLine
}

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, PrintOnly: true, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget}}
}
