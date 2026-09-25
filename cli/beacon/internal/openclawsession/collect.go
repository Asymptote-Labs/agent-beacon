package openclawsession

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
	LastOrder   int   `json:"last_order"`
	UpdatedAtMS int64 `json:"updated_at_ms,omitempty"`
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
		return nil, fmt.Errorf("read OpenClaw collector state %s: %w", path, err)
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
	return ref.Profile + ":" + ref.ID + ":" + ref.SourcePath
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "openclaw-sessions.json")
}

type CollectOptions struct {
	OpenClawDir string
	StatePath   string
	Write       bool
	LogPath     string
	UserMode    bool
	Print       bool
	Out         io.Writer
}

type Summary struct {
	Traces        int `json:"traces"`
	TracesChanged int `json:"traces_changed"`
	EventsEmitted int `json:"events_emitted"`
	Errors        int `json:"errors"`
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	store, err := NewStore(opts.OpenClawDir)
	if err != nil {
		return summary, err
	}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	summary.Traces = len(refs)
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save OpenClaw collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectTrace(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("OpenClaw trace %s: %w", ref.ID, collectErr))
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

func collectTrace(store *Store, ref TraceRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	cursor := state.cursor(ref)
	if ref.UpdatedAtUnixMS > 0 && ref.UpdatedAtUnixMS < cursor.UpdatedAtMS {
		return false, nil
	}
	records, err := store.Read(ref)
	if err != nil {
		return false, err
	}
	mapped := MapTrace(ref, records, MapOptions{MinOrder: cursor.LastOrder})
	if len(mapped) == 0 {
		if len(records) > 0 && records[len(records)-1].Order > cursor.LastOrder {
			cursor.LastOrder = records[len(records)-1].Order
		}
		cursor.UpdatedAtMS = ref.UpdatedAtUnixMS
		return false, nil
	}
	start := cursor.LastOrder
	for i, item := range mapped {
		if err := emitEvent(item.Event, opts); err != nil {
			cursor.LastOrder = partialLastOrder(start, mapped, i)
			return true, err
		}
		cursor.LastOrder = item.SourceOrder
		summary.EventsEmitted++
	}
	cursor.UpdatedAtMS = ref.UpdatedAtUnixMS
	return true, nil
}

// partialLastOrder is where the cursor stands after the write of mapped[failedIdx] failed: past
// every record whose events were all written, so the next sweep retries the record that failed.
// Stopping at the last written event's record would lose the rest of that record's events for
// good once a record maps to more than one. start is the cursor before this sweep; the cursor
// never moves back past it.
func partialLastOrder(start int, mapped []MappedEvent, failedIdx int) int {
	for i := failedIdx - 1; i >= 0; i-- {
		if mapped[i].SourceOrder != mapped[failedIdx].SourceOrder {
			if mapped[i].SourceOrder > start {
				return mapped[i].SourceOrder
			}
			break
		}
	}
	return start
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
