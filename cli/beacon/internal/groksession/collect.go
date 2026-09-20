package groksession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

const StateVersion = 1

// Cursor records how far Beacon has read one Grok session.
//
// A Grok session directory holds two append-only logs that grow independently, so the cursor keeps
// a line position for each rather than a single "how far did we get" number. Remembering only the
// session's modification time and a total event count would make every sweep after the session grew
// -- a --watch tick, tomorrow's sync -- re-map the session from its first line and append a full
// duplicate set to the runtime log.
type Cursor struct {
	// UpdatedAtMS is the session's last-seen modification time, used to skip sessions that cannot
	// have changed. It is a cheap pre-filter, not the record of what was written.
	UpdatedAtMS int64 `json:"updated_at_ms,omitempty"`
	// Events is the running total written for this session, across all sweeps.
	Events int `json:"events,omitempty"`
	// ChatLine and LifecycleLine are the last line of chat_history.jsonl and events.jsonl whose
	// events were all written. Lines are physical line numbers, so a half-written line at the tail
	// of a live session does not shift the positions of the lines before it.
	ChatLine      int `json:"chat_line,omitempty"`
	LifecycleLine int `json:"lifecycle_line,omitempty"`
	// Started records that session.started was written. It has no line of its own, being derived
	// from summary.json and the first turn_started row.
	Started bool `json:"started,omitempty"`
}

type State struct {
	Version  int                `json:"version"`
	Sessions map[string]*Cursor `json:"sessions"`
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "grok-sessions.json")
}

func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Sessions: map[string]*Cursor{}}
	if path == "" {
		return state, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return nil, err
	}
	var stored State
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("read Grok session collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Sessions == nil {
		return state, nil
	}
	return &stored, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Cursor{}
	}
	s.Version = StateVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *State) cursor(id string) *Cursor {
	if s.Sessions == nil {
		s.Sessions = map[string]*Cursor{}
	}
	c := s.Sessions[id]
	if c == nil {
		c = &Cursor{}
		s.Sessions[id] = c
	}
	return c
}

type CollectOptions struct {
	SessionsDir string
	StatePath   string
	LogPath     string
	Write       bool
	UserMode    bool
	Print       bool
	Out         io.Writer
}

type SummaryResult struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	MalformedLines  int `json:"malformed_lines"`
}

func CollectOnce(opts CollectOptions) (summary SummaryResult, err error) {
	if opts.Print && !opts.Write {
		opts.StatePath = ""
	}
	store, err := NewStore(opts.SessionsDir)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
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
			err = fmt.Errorf("save Grok session collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Grok session %s: %w", ref.ID, collectErr))
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

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, summary *SummaryResult) (bool, error) {
	cursor := state.cursor(ref.ID)
	if cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
		return false, nil
	}
	data, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if err != nil {
		return false, err
	}
	mapped := MapSession(data, MapOptions{
		MinChatLine:      cursor.ChatLine,
		MinLifecycleLine: cursor.LifecycleLine,
		SkipStarted:      cursor.Started,
	})
	if len(mapped) == 0 {
		cursor.UpdatedAtMS = ref.ModTimeUnixMS
		return false, nil
	}
	written := 0
	for _, item := range mapped {
		if err := emit(item.Event, opts); err != nil {
			commit(cursor, mapped, written)
			return true, err
		}
		written++
		summary.EventsEmitted++
	}
	commit(cursor, mapped, written)
	cursor.UpdatedAtMS = ref.ModTimeUnixMS
	return true, nil
}

// commit advances the cursor over the events that were actually written.
//
// One source line can produce several events -- an assistant line yields reasoning, a message and
// one event per tool call -- so a line counts as collected only once every event it produced has
// been written. A line with an unwritten event left on it stays behind the cursor, and the next
// sweep re-emits the whole line rather than silently dropping its tail. On a clean pass there is
// nothing unwritten and every line advances.
func commit(cursor *Cursor, mapped []MappedEvent, written int) {
	blocked := map[SourceKind]int{}
	for _, item := range mapped[written:] {
		if line, seen := blocked[item.SourceKind]; !seen || item.SourceLine < line {
			blocked[item.SourceKind] = item.SourceLine
		}
	}
	for _, item := range mapped[:written] {
		if item.SourceKind == SourceSession {
			cursor.Started = true
			continue
		}
		if line, seen := blocked[item.SourceKind]; seen && item.SourceLine >= line {
			continue
		}
		switch item.SourceKind {
		case SourceChat:
			if item.SourceLine > cursor.ChatLine {
				cursor.ChatLine = item.SourceLine
			}
		case SourceLifecycle:
			if item.SourceLine > cursor.LifecycleLine {
				cursor.LifecycleLine = item.SourceLine
			}
		}
	}
	cursor.Events += written
}

func emit(event schema.Event, opts CollectOptions) error {
	if opts.Print && opts.Out != nil {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := opts.Out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if opts.Write {
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return err
		}
	}
	return nil
}
