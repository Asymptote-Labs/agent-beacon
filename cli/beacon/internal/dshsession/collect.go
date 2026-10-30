package dshsession

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionwindow"
)

type Cursor struct {
	LastLine     int   `json:"last_line"`
	Started      bool  `json:"started"`
	SizeBytes    int64 `json:"size_bytes,omitempty"`
	ModTimeMS    int64 `json:"mod_time_ms,omitempty"`
	Events       int   `json:"events,omitempty"`
	PartialTail  bool  `json:"partial_tail,omitempty"`
	PartialFrame bool  `json:"partial_frame,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Sources map[string]*Cursor `json:"sources"`
}

type CollectOptions struct {
	DSHHome   string
	StatePath string
	Write     bool
	LogPath   string
	UserMode  bool
	Print     bool
	Out       io.Writer
	Workspace string
	SessionID string
	// Since, when set, limits the sweep to sessions modified at or after it, newest first. Older
	// sessions are not read and their cursors do not move, so a later sync still collects them.
	// The install-time backfill sets it; an ordinary sync leaves it zero.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
	// RotateBytes is the size the runtime log rotates at while this sweep writes it; zero is
	// writer.DefaultRotateBytes.
	RotateBytes int64

	// guard keeps the sweep from rotating its own output out of the log; CollectOnce sets it.
	guard *writer.RetentionGuard
}

type Summary struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	MalformedLines  int `json:"malformed_lines"`
	PartialSessions int `json:"partial_sessions"`
	sessionsync.Retention
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "dsh.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "DeepSeek",
	New:   func() *State { return &State{Version: StateVersion, Sources: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Sources != nil },
	Stamp: func(s *State) { s.Version = StateVersion },
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

func (s *State) cursor(source string) *Cursor {
	if s.Sources == nil {
		s.Sources = map[string]*Cursor{}
	}
	cursor := s.Sources[source]
	if cursor == nil {
		cursor = &Cursor{}
		s.Sources[source] = cursor
	}
	return cursor
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	store, err := NewStore(opts.DSHHome)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	refs = filterRefs(refs, opts)
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
			err = fmt.Errorf("save DeepSeek collector state: %w", saveErr)
		}
	}()
	var errs []error
	opts.guard = sessionsync.NewRetentionGuard(opts.Print, opts.Write)
	var stopErr error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if sessionsync.Stopped(collectErr) {
			// Not a failed session: this one and the rest are left for a later sync, with cursors
			// that have not moved past anything that was not written.
			if changed {
				summary.SessionsChanged++
			}
			stopErr = collectErr
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("DeepSeek session %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	if rotatedOut := summary.Finish(opts.sink(), stopErr); rotatedOut != nil {
		errs = append(errs, rotatedOut)
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	sourceKey := ref.Path
	cursor := state.cursor(sourceKey)
	if cursor.ModTimeMS == ref.ModTimeUnixMS && cursor.SizeBytes == ref.SizeBytes && !cursor.PartialTail && !cursor.PartialFrame {
		return false, nil
	}
	if ref.SizeBytes < cursor.SizeBytes {
		cursor.LastLine = 0
		cursor.Started = false
	}
	records, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if stats.PartialTail || stats.PartialFrame {
		summary.PartialSessions++
	}
	if err != nil {
		return false, err
	}
	mapped := MapSession(ref, records, MapOptions{MinLine: cursor.LastLine, SkipSessionStarted: cursor.Started})
	if len(mapped) == 0 {
		advanceCursor(cursor, ref, records, stats)
		return false, nil
	}
	for i, item := range mapped {
		if err := emitEvent(item.Event, opts); err != nil {
			advanceCursorPartial(cursor, mapped, i)
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
		cursor.Events++
	}
	advanceCursor(cursor, ref, records, stats)
	return true, nil
}

func advanceCursor(cursor *Cursor, ref SessionRef, records []Record, stats Stats) {
	if len(records) > 0 {
		cursor.LastLine = records[len(records)-1].Line
	}
	cursor.SizeBytes = ref.SizeBytes
	cursor.ModTimeMS = ref.ModTimeUnixMS
	cursor.PartialTail = stats.PartialTail
	cursor.PartialFrame = stats.PartialFrame
	for _, record := range records {
		if record.Kind == "session" && record.Line <= cursor.LastLine {
			cursor.Started = true
			break
		}
	}
}

// advanceCursorPartial moves the cursor past source lines whose mapped events were all emitted, so
// the next sweep retries from the source line that failed. One line maps to several events (an
// assistant message's reasoning, text and token usage); stopping at the last emitted event's line
// would skip the rest of that line for good. failedIdx is the index into mapped of the event whose
// emit failed.
func advanceCursorPartial(cursor *Cursor, mapped []MappedEvent, failedIdx int) {
	for i := 0; i < failedIdx; i++ {
		if mapped[i].Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	for i := failedIdx - 1; i >= 0; i-- {
		if mapped[i].SourceLine != mapped[failedIdx].SourceLine {
			if mapped[i].SourceLine > cursor.LastLine {
				cursor.LastLine = mapped[i].SourceLine
			}
			return
		}
	}
}

// emitEvent emits through opts.sink(), swapped by tests to fail a chosen write.
var emitEvent = func(event schema.Event, opts CollectOptions) error { return opts.sink().Emit(event) }

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget, Guard: o.guard}}
}

func filterRefs(refs []SessionRef, opts CollectOptions) []SessionRef {
	sessionID := strings.TrimSpace(opts.SessionID)
	workspace := cleanPath(opts.Workspace)
	if sessionID == "" && workspace == "" {
		return refs
	}
	out := refs[:0]
	for _, ref := range refs {
		if sessionID != "" && ref.ID != sessionID {
			continue
		}
		if workspace != "" {
			cwd := ""
			if ref.Meta != nil {
				cwd = cleanPath(ref.Meta.CWD)
			}
			if cwd != workspace {
				continue
			}
		}
		out = append(out, ref)
	}
	return out
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}
