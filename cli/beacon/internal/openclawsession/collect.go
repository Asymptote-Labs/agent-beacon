package openclawsession

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
	LastOrder   int   `json:"last_order"`
	UpdatedAtMS int64 `json:"updated_at_ms,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Traces  map[string]*Cursor `json:"traces"`
}

var stateFile = sessionsync.StateFile[State]{
	Name:      "OpenClaw",
	New:       func() *State { return &State{Version: StateVersion, Traces: map[string]*Cursor{}} },
	Valid:     func(s *State) bool { return s.Version == StateVersion && s.Traces != nil },
	Stamp:     func(s *State) { s.Version = StateVersion },
	BlankPath: true,
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

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
	// Since, when set, limits the sweep to sessions modified at or after it, newest first. Older
	// sessions are not read and their cursors do not move, so a later sync still collects them.
	// The install-time backfill sets it; an ordinary sync leaves it zero.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
	// RotateBytes is the size the runtime log rotates at while this sweep writes it; zero is
	// writer.DefaultRotateBytes.
	RotateBytes int64

	// guard keeps the sweep from rotating its own output out of the log; CollectOnce sets it.
	guard *writer.RetentionGuard
}

type Summary struct {
	Traces        int `json:"traces"`
	TracesChanged int `json:"traces_changed"`
	EventsEmitted int `json:"events_emitted"`
	Errors        int `json:"errors"`
	sessionsync.Retention
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
	refs = sessionwindow.Recent(refs, opts.Since, func(r TraceRef) int64 { return r.UpdatedAtUnixMS })
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
	opts.guard = sessionsync.NewRetentionGuard(opts.Print, opts.Write)
	var stopErr error
	for _, ref := range refs {
		changed, collectErr := collectTrace(store, ref, state, opts, &summary)
		if sessionsync.Stopped(collectErr) {
			// Not a failed session: this one and the rest are left for a later sync, with cursors
			// that have not moved past anything that was not written.
			if changed {
				summary.TracesChanged++
			}
			stopErr = collectErr
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("OpenClaw trace %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.TracesChanged++
		}
	}
	if rotatedOut := summary.Finish(opts.sink(), stopErr); rotatedOut != nil {
		errs = append(errs, rotatedOut)
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

// emitEvent emits through opts.sink(), swapped by tests to fail a chosen write.
var emitEvent = func(event schema.Event, opts CollectOptions) error { return opts.sink().Emit(event) }

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget, Guard: o.guard}}
}
