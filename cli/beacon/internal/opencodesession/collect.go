package opencodesession

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

const StateVersion = 1

// stateCheckpointInterval is how often a sweep saves its cursors while it runs. A first sync over a
// long history can take minutes, and a sweep stopped before it returns otherwise keeps nothing.
const stateCheckpointInterval = 5 * time.Second

type Cursor struct {
	LastOrder   int             `json:"last_order"`
	UpdatedAtMS int64           `json:"updated_at_ms,omitempty"`
	Emitted     map[string]bool `json:"emitted,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Traces  map[string]*Cursor `json:"traces"`
}

var stateFile = sessionsync.StateFile[State]{
	Name:      "OpenCode",
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
	// Since, when set, limits the sweep to sessions modified at or after it, newest first. Older
	// sessions are not read and their cursors do not move, so a later sync still collects them.
	// The install-time backfill sets it; an ordinary sync leaves it zero.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
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
	refs = sessionwindow.Recent(refs, opts.Since, func(r TraceRef) int64 { return r.UpdatedAtUnixMS })
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
	saved := time.Now()
	for _, ref := range refs {
		if time.Since(saved) >= stateCheckpointInterval {
			if err := state.Save(opts.StatePath); err == nil {
				saved = time.Now()
			}
		}
		changed, collectErr := collectTrace(store, ref, state, opts, &summary)
		if errors.Is(collectErr, writer.ErrBudgetSpent) {
			// Not a failed session: this one and the rest are left for a later sync, with cursors
			// that have not moved past anything that was not written.
			if changed {
				summary.TracesChanged++
			}
			errs = append(errs, collectErr)
			break
		}
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
		if err := opts.sink().Emit(item.Event); err != nil {
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

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, Budget: o.Budget}}
}
