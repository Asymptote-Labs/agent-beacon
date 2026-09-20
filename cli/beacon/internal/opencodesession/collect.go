package opencodesession

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
	LastOrder   int             `json:"last_order"`
	UpdatedAtMS int64           `json:"updated_at_ms,omitempty"`
	Emitted     map[string]bool `json:"emitted,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Traces  map[string]*Cursor `json:"traces"`
}

func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Traces: map[string]*Cursor{}}
	if strings.TrimSpace(path) == "" {
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
		return nil, fmt.Errorf("read OpenCode collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Traces == nil {
		return state, nil
	}
	return &stored, nil
}

func (s *State) Save(path string) error {
	if strings.TrimSpace(path) == "" {
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

func (s *State) cursor(ref TraceRef) *Cursor {
	if s.Traces == nil {
		s.Traces = map[string]*Cursor{}
	}
	key := stateKey(ref)
	cursor := s.Traces[key]
	if cursor == nil {
		cursor = &Cursor{}
		s.Traces[key] = cursor
	}
	return cursor
}

func stateKey(ref TraceRef) string {
	return string(ref.Kind) + ":" + ref.ID
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "opencode-sessions.json")
}

type CollectOptions struct {
	DataDir   string
	StatePath string
	Write     bool
	LogPath   string
	UserMode  bool
	Print     bool
	Out       io.Writer
}

type Summary struct {
	Traces        int `json:"traces"`
	TracesChanged int `json:"traces_changed"`
	EventsEmitted int `json:"events_emitted"`
	Errors        int `json:"errors"`
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	store, err := NewStore(opts.DataDir)
	if err != nil {
		return summary, err
	}
	refs, listErr := store.List()
	if listErr != nil && len(refs) == 0 {
		return summary, listErr
	}
	summary.Traces = len(refs)
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save OpenCode collector state: %w", saveErr)
		}
	}()

	var errs []error
	if listErr != nil {
		summary.Errors++
		errs = append(errs, listErr)
	}
	for _, ref := range refs {
		changed, collectErr := collectTrace(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("OpenCode trace %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.TracesChanged++
		}
	}
	if len(errs) > 0 {
		return summary, errors.Join(errs...)
	}
	return summary, nil
}

// collectTrace writes whatever this trace still owes the runtime log.
//
// What Beacon has already seen is tracked by event id rather than by how far it read, because
// OpenCode's store is not an append-only file. A tool part is rewritten in place when the call
// returns, and a record's position is recomputed on every read -- a part that only becomes mappable
// later, such as an assistant text part still streaming in, renumbers every record after it. A
// high-water mark over those positions both skips the completion of a tool call, which is the event
// carrying its output, exit code and diff, and re-emits whatever the renumbering shifted.
//
// Event ids are derived from the record's own identity (see mapper.append), so the same action maps
// to the same id on every sweep and the set below is all the collector needs to tell a new action
// from one already written.
func collectTrace(store *Store, ref TraceRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	cursor := state.cursor(ref)
	// Only a trace whose store timestamp moved backwards is skipped outright. An unchanged
	// timestamp is not evidence that nothing changed: OpenCode finishing a tool call rewrites the
	// part without necessarily touching the session's time_updated.
	if ref.UpdatedAtUnixMS > 0 && ref.UpdatedAtUnixMS < cursor.UpdatedAtMS {
		return false, nil
	}
	records, err := store.Read(ref)
	if err != nil {
		return false, err
	}
	mapped := MapTrace(ref, records, MapOptions{})
	if cursor.Emitted == nil {
		cursor.Emitted = make(map[string]bool, len(mapped))
	}
	changed := false
	live := make(map[string]bool, len(mapped))
	for _, item := range mapped {
		id := item.Event.Event.ID
		live[id] = true
		if cursor.Emitted[id] {
			continue
		}
		if err := emit(item.Event, opts); err != nil {
			// The ids already written stay recorded, so a retry resumes rather than doubling the
			// log. The trace's timestamp is left alone so the next sweep comes back for the rest.
			return changed, err
		}
		cursor.Emitted[id] = true
		if item.SourceOrder > cursor.LastOrder {
			cursor.LastOrder = item.SourceOrder
		}
		summary.EventsEmitted++
		changed = true
	}
	// Ids the trace no longer produces are dropped, so the cursor stays proportional to the session
	// OpenCode is actually holding rather than to everything it has ever held. Compaction, which
	// removes messages outright, is the case that makes this matter.
	for id := range cursor.Emitted {
		if !live[id] {
			delete(cursor.Emitted, id)
		}
	}
	cursor.UpdatedAtMS = ref.UpdatedAtUnixMS
	return changed, nil
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
