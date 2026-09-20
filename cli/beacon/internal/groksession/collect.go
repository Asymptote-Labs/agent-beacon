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

type Cursor struct {
	UpdatedAtMS        int64 `json:"updated_at_ms,omitempty"`
	Events             int   `json:"events,omitempty"`
	LastChatIndex      int   `json:"last_chat_index,omitempty"`
	LastLifecycleIndex int   `json:"last_lifecycle_index,omitempty"`
	SessionStarted     bool  `json:"session_started,omitempty"`
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
		MinChatIndex:       cursor.LastChatIndex,
		MinLifecycleIndex:  cursor.LastLifecycleIndex,
		SkipSessionStarted: cursor.SessionStarted,
	})
	if len(mapped) == 0 {
		cursor.UpdatedAtMS = ref.ModTimeUnixMS
		advanceGrokCursor(cursor, data)
		return false, nil
	}
	for _, item := range mapped {
		if err := emit(item.Event, opts); err != nil {
			return true, err
		}
		summary.EventsEmitted++
	}
	cursor.UpdatedAtMS = ref.ModTimeUnixMS
	cursor.Events += len(mapped)
	advanceGrokCursor(cursor, data)
	return true, nil
}

func advanceGrokCursor(cursor *Cursor, data SessionData) {
	if n := len(data.Chat); n > cursor.LastChatIndex {
		cursor.LastChatIndex = n
	}
	if n := len(data.Lifecycle); n > cursor.LastLifecycleIndex {
		cursor.LastLifecycleIndex = n
	}
	cursor.SessionStarted = true
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
