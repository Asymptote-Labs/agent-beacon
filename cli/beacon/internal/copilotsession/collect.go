package copilotsession

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

type Cursor struct {
	LastLine      int   `json:"last_line"`
	SizeBytes     int64 `json:"size_bytes,omitempty"`
	ModTimeUnixMS int64 `json:"mtime_unix_ms,omitempty"`
	Started       bool  `json:"started,omitempty"`
	PartialTail   bool  `json:"partial_tail,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Files   map[string]*Cursor `json:"files"`
}

type CollectOptions struct {
	CopilotDir string
	StatePath  string
	Write      bool
	LogPath    string
	UserMode   bool
	Print      bool
	Out        io.Writer
	Workspace  string
	SessionID  string
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
	return filepath.Join(base, "endpoint", "state", "copilot.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "GitHub Copilot CLI",
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
	if opts.Print {
		opts.StatePath = ""
		opts.Write = false
	}
	store, err := NewStore(opts.CopilotDir)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	refs = FilterRefs(refs, opts)
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
			err = fmt.Errorf("save GitHub Copilot CLI collector state: %w", saveErr)
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
			errs = append(errs, fmt.Errorf("GitHub Copilot CLI session %s: %w", ref.ID, collectErr))
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
	if ref.SizeBytes == cursor.SizeBytes && ref.ModTimeUnixMS == cursor.ModTimeUnixMS && !cursor.PartialTail {
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
		advanceCursor(cursor, ref, records, stats)
		return false, nil
	}
	for i, item := range mapped {
		if err := opts.sink().Emit(item.Event); err != nil {
			advanceCursorPartial(cursor, mapped, i)
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	advanceCursor(cursor, ref, records, stats)
	return true, nil
}

func advanceCursor(cursor *Cursor, ref SessionRef, records []Record, stats Stats) {
	if len(records) > 0 {
		cursor.LastLine = records[len(records)-1].Line
	}
	cursor.SizeBytes = ref.SizeBytes
	cursor.ModTimeUnixMS = ref.ModTimeUnixMS
	cursor.PartialTail = stats.PartialTail
	for _, record := range records {
		if record.Type == "session.start" && record.Line <= cursor.LastLine {
			cursor.Started = true
			break
		}
	}
}

func advanceCursorPartial(cursor *Cursor, mapped []MappedEvent, failedIdx int) {
	var lastLine int
	failedLine := mapped[failedIdx].SourceLine
	for i := 0; i < failedIdx; i++ {
		if mapped[i].SourceLine < failedLine && mapped[i].SourceLine > lastLine {
			lastLine = mapped[i].SourceLine
		}
		if mapped[i].SourceLine < failedLine && mapped[i].Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	if lastLine > cursor.LastLine {
		cursor.LastLine = lastLine
	}
}

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, Budget: o.Budget}}
}

func FilterRefs(refs []SessionRef, opts CollectOptions) []SessionRef {
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
		if workspace != "" && cleanPath(workspaceOf(ref)) != workspace {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func workspaceOf(ref SessionRef) string {
	if ref.Meta == nil {
		return ""
	}
	if strings.TrimSpace(ref.Meta.CWD) != "" {
		return ref.Meta.CWD
	}
	return ref.Meta.GitRoot
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
