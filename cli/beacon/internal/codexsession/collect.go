package codexsession

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
	CodexDir  string
	StatePath string
	Write     bool
	LogPath   string
	UserMode  bool
	Print     bool
	Out       io.Writer
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
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "codex.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Codex",
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
	store, err := NewStore(opts.CodexDir)
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
			err = fmt.Errorf("save Codex collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
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
			errs = append(errs, fmt.Errorf("Codex session %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
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
	mapped := MapSession(ref, records, MapOptions{MinLine: cursor.LastLine, SkipSessionStarted: cursor.Started})
	if len(mapped) == 0 {
		advanceCursor(cursor, ref, stats.Lines)
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
	}
	advanceCursor(cursor, ref, stats.Lines)
	return true, nil
}

func advanceCursor(cursor *Cursor, ref SessionRef, lines int) {
	cursor.LastLine = lines
	cursor.SizeBytes = ref.SizeBytes
	cursor.ModTimeUnixMS = ref.ModTimeUnixMS
}

// emitEvent emits through opts.sink(), swapped by tests to fail a chosen write.
var emitEvent = func(event schema.Event, opts CollectOptions) error { return opts.sink().Emit(event) }

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, PrintOnly: true, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, Budget: o.Budget}}
}

// advanceCursorPartial moves the cursor past source lines whose mapped events were all emitted, so
// the next sweep retries from the source line that failed. One rollout line can map to several
// events (a prompt and its session.handoff link); stopping at the last emitted event's line would
// skip the rest of that line for good. failedIdx is the index into mapped of the event whose emit
// failed. This is the claudesession rule.
func advanceCursorPartial(cursor *Cursor, mapped []MappedEvent, failedIdx int) {
	for i := failedIdx - 1; i >= 0; i-- {
		if mapped[i].SourceLine != mapped[failedIdx].SourceLine {
			if mapped[i].SourceLine > cursor.LastLine {
				cursor.LastLine = mapped[i].SourceLine
			}
			return
		}
	}
}
