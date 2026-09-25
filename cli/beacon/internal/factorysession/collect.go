package factorysession

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
	SourcePath     string `json:"source_path,omitempty"`
	LastLine       int    `json:"last_line"`
	Started        bool   `json:"started"`
	ModTimeUnixMS  int64  `json:"mtime_ms,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	SettingsUnixMS int64  `json:"settings_mtime_ms,omitempty"`
}

type State struct {
	Version  int                `json:"version"`
	Sessions map[string]*Cursor `json:"sessions"`
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
		return nil, fmt.Errorf("read Factory collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Sessions == nil {
		return state, nil
	}
	state.Sessions = stored.Sessions
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

func (s *State) cursor(sessionID string) *Cursor {
	if s.Sessions == nil {
		s.Sessions = map[string]*Cursor{}
	}
	cursor := s.Sessions[sessionID]
	if cursor == nil {
		cursor = &Cursor{}
		s.Sessions[sessionID] = cursor
	}
	return cursor
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "factory.json")
}

type CollectOptions struct {
	SessionsDir string
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

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
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
			err = fmt.Errorf("save Factory collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Factory session %s: %w", ref.ID, collectErr))
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
	cursor := state.cursor(ref.ID)
	if cursor.SourcePath != "" && cursor.SourcePath != ref.Path {
		cursor.LastLine = 0
		cursor.Started = false
		cursor.SettingsUnixMS = 0
	}
	if cursor.LastLine > 0 &&
		cursor.ModTimeUnixMS == ref.ModTimeUnixMS &&
		cursor.SizeBytes == ref.SizeBytes &&
		cursor.SettingsUnixMS == ref.SettingsUnixMS {
		return false, nil
	}
	records, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if stats.Partial {
		summary.PartialSessions++
	}
	if err != nil {
		return false, err
	}
	emitSettingsUsage := ref.Settings != nil && ref.SettingsUnixMS > 0 && ref.SettingsUnixMS != cursor.SettingsUnixMS
	mapped := MapSession(ref, records, MapOptions{
		MinLine:            cursor.LastLine,
		SkipSessionStarted: cursor.Started,
		EmitSettingsUsage:  emitSettingsUsage,
	})
	for i, item := range mapped {
		if err := emitEvent(item.Event, opts); err != nil {
			advanceCursorPartial(cursor, ref, mapped, i)
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	advanceCursor(cursor, ref, stats)
	return len(mapped) > 0 || stats.Lines != cursor.LastLine || emitSettingsUsage, nil
}

func advanceCursor(cursor *Cursor, ref SessionRef, stats ReadStats) {
	cursor.SourcePath = ref.Path
	cursor.LastLine = stats.Lines
	cursor.ModTimeUnixMS = ref.ModTimeUnixMS
	cursor.SizeBytes = ref.SizeBytes
	cursor.SettingsUnixMS = ref.SettingsUnixMS
}

// advanceCursorPartial moves the cursor past source lines whose mapped events were all emitted, so
// the next sweep retries from the source line that failed. One line maps to several events (a
// message's text blocks, a tool call and its result); stopping at the last emitted event's line
// would skip the rest of that line for good. failedIdx is the index into mapped of the event whose
// emit failed.
func advanceCursorPartial(cursor *Cursor, ref SessionRef, mapped []MappedEvent, failedIdx int) {
	cursor.SourcePath = ref.Path
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

// emitEvent is emit, swapped by tests to fail a chosen write.
var emitEvent = emit

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
