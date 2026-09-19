package claudesession

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
	return filepath.Join(base, "endpoint", "state", "claude.json")
}

func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Files: map[string]*Cursor{}}
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
		return nil, fmt.Errorf("read Claude collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Files == nil {
		return state, nil
	}
	state.Files = stored.Files
	return state, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	s.Version = StateVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

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

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Claude session %s: %w", ref.ID, collectErr))
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
		if err := emit(item.Event, opts); err != nil {
			if i > 0 {
				cursor.LastLine = mapped[i-1].SourceLine
			}
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

func emit(event schema.Event, opts CollectOptions) error {
	if opts.Print {
		out := opts.Out
		if out == nil {
			out = io.Discard
		}
		return json.NewEncoder(out).Encode(event)
	}
	if !opts.Write {
		return nil
	}
	_, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode})
	return err
}
