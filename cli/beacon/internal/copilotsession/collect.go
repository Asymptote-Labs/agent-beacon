package copilotsession

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
		return nil, fmt.Errorf("read GitHub Copilot CLI collector state %s: %w", path, err)
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
		if err := emit(item.Event, opts); err != nil {
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
	for i := 0; i < failedIdx; i++ {
		if mapped[i].SourceLine > lastLine {
			lastLine = mapped[i].SourceLine
		}
		if mapped[i].Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	if lastLine > cursor.LastLine {
		cursor.LastLine = lastLine
	}
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
