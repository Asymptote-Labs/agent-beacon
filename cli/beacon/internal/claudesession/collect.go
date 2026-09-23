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
	// RotateBytes is the size at which this sweep rotates the runtime log; zero means the size
	// every Beacon writer uses. The archive count is not configurable here: the hooks and the
	// collector prune archives past the shared count on their next rotation, and readers only
	// look that far, so a sweep that kept more would lose them anyway.
	RotateBytes int64
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

	var guard *writer.RetentionGuard
	if opts.Write && !opts.Print {
		guard = &writer.RetentionGuard{}
	}
	var errs []error
	for i, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, guard, &summary)
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
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Claude session %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
		}
	}
	if guard != nil {
		summary.EventsRetained = guard.Retained()
		summary.EventsRotatedOut = guard.Written() - summary.EventsRetained
		if summary.EventsRotatedOut > 0 {
			errs = append(errs, fmt.Errorf("%d of the %d events this sweep wrote were rotated out of the runtime log by another writer before it finished",
				summary.EventsRotatedOut, guard.Written()))
		}
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

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, guard *writer.RetentionGuard, summary *Summary) (bool, error) {
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
		if err := emit(item.Event, opts, guard); err != nil {
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
// an error.
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
	cursor.LastLine = lastCompleteLine
}

func emit(event schema.Event, opts CollectOptions, guard *writer.RetentionGuard) error {
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
	_, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode, RotateSize: opts.RotateBytes, Guard: guard})
	return err
}
