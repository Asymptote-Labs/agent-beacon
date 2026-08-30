package fxsession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// StateVersion is the on-disk version of the collector's cursor file. It exists so a future change
// to what a cursor holds can reset the file rather than misread it: re-collecting a session is
// cheap and duplicates are suppressed downstream, while misreading a cursor loses events silently.
const StateVersion = 1

// Cursor is what the collector remembers about one fx session between sweeps.
//
// Generation is fx's log generation and LastSeq is the last sequence collected within it. Both are
// needed because fx restarts the sequence at 1 whenever it compacts a session's log -- a cursor
// holding only a sequence would silently skip the first events of every compacted session.
type Cursor struct {
	Generation string `json:"generation"`
	LastSeq    uint64 `json:"last_seq"`
	// Started records that a session.started has already been reported. fx writes a fresh
	// session_started at sequence 1 of every generation, including the one compaction creates,
	// and a session that never restarted must not be reported as starting twice.
	Started bool `json:"started"`
	// UpdatedAtMS is the manifest's updated_at at the last full sweep, kept only as a cheap
	// "nothing changed" check. It is never the thing that decides what to emit.
	UpdatedAtMS int64 `json:"updated_at_ms,omitempty"`
}

// State is the collector's cursor file: one cursor per fx session.
type State struct {
	Version  int                `json:"version"`
	Sessions map[string]*Cursor `json:"sessions"`
}

// LoadState reads the cursor file. A missing file is an empty state, which is what a first run
// looks like. An empty path yields state that is never persisted, which is what --print uses.
//
// A file from another state version is discarded rather than parsed: re-collecting a session
// appends events that the writer's own duplicate window and the deterministic event ids already
// make harmless, while reading a cursor under the wrong meaning skips events for good.
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
		return nil, fmt.Errorf("read fx collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Sessions == nil {
		return state, nil
	}
	state.Sessions = stored.Sessions
	return state, nil
}

// Save writes the cursor file atomically. An empty path is a no-op.
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
	c := s.Sessions[sessionID]
	if c == nil {
		c = &Cursor{}
		s.Sessions[sessionID] = c
	}
	return c
}

// CollectOptions configures one sweep over fx's session store.
type CollectOptions struct {
	// SessionsDir is fx's session directory. Empty means fx's default location.
	SessionsDir string
	// StatePath is the cursor file. Empty disables persistence, which makes the sweep a dry run
	// that re-reads everything next time.
	StatePath string
	// Write appends mapped events to the runtime JSONL at LogPath.
	Write bool
	// LogPath is the runtime JSONL. Empty means the writer's default for UserMode.
	LogPath  string
	UserMode bool
	// Print writes each mapped event to Out as JSON. Used by --print, which pairs with an empty
	// StatePath so a dry run shows the same events every time.
	Print bool
	Out   io.Writer
}

// Summary reports what a sweep did. Malformed and PartialSessions are reported rather than logged
// because "Beacon collected nothing" and "Beacon could not read this" look identical from outside.
type Summary struct {
	Sessions        int
	SessionsChanged int
	EventsEmitted   int
	Errors          int
	MalformedLines  int
	PartialSessions int
}

// CollectOnce performs one sweep: list fx's sessions, read the ones that moved since the last
// sweep, map their new records, and emit them.
//
// Idempotent by construction. The cursor advances only over records that were actually emitted, so
// a sweep that fails partway leaves the rest to be retried; and because the cursor is a position in
// fx's own log rather than a set of ids, its size does not grow with the session.
//
// One session's failure does not end the sweep. A session directory can be unreadable for ordinary
// reasons -- another user owns it, fx is mid-write, a disk error -- and none of them is a reason to
// collect nothing from the other sessions on the machine.
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
	// Progress is saved however this returns. Events are appended as each session is processed, so
	// a later session's failure must not throw away the cursor advances already earned -- otherwise
	// the next sweep re-emits everything the failed sweep already wrote. err is a named return so a
	// failure to persist the cursor is reported rather than swallowed: an unsaved sweep re-emits
	// everything it just wrote.
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save fx collector state: %w", saveErr)
		}
	}()

	var errs []error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("fx session %s: %w", ref.ID, collectErr))
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

	// The manifest is fx's own summary of what it has committed, so when it says the session has
	// not moved past the cursor there is nothing to read. Skipping on it keeps a sweep over a
	// machine with hundreds of old sessions proportional to what changed rather than to what
	// exists. A session with no readable manifest is always read: there is nothing cheaper to
	// trust, and reading it is correct, just not free.
	if ref.Manifest != nil && ref.Manifest.LogGeneration == cursor.Generation &&
		ref.Manifest.LastEventSeq <= cursor.LastSeq {
		return false, nil
	}

	events, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if stats.PartialTail {
		summary.PartialSessions++
	}
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}

	minSeq := cursor.LastSeq
	if cursor.Generation != "" && cursor.Generation != generationOf(events) {
		// fx compacted this session's log: the sequence restarted, and the turns that were in the
		// old log are not in the new one -- compaction rewrites it as a session_started plus an
		// opaque state blob. So everything in the new generation is new, and starting from zero
		// re-reads nothing that was already collected.
		//
		// The honest limitation of reading a log rather than being handed events: a turn committed
		// after the last sweep and before a compaction is folded into that blob and is gone. fx
		// compacts on log growth, so this needs a long gap between sweeps and a lot of activity in
		// it; sweeping on a schedule (the watch mode of `beacon endpoint fx sync`) is what keeps
		// the window small.
		minSeq = 0
	}

	mapped := MapSession(ref, events, MapOptions{MinSeq: minSeq, SkipSessionStarted: cursor.Started})
	if len(mapped) == 0 {
		advanceCursor(cursor, ref, events)
		return false, nil
	}

	for _, item := range mapped {
		if err := emit(item.Event, opts); err != nil {
			// The cursor is deliberately not advanced here. The events already written stay
			// written, and the next sweep re-reads from the last confirmed position -- which the
			// writer's duplicate window and the deterministic event ids make safe.
			return true, err
		}
		summary.EventsEmitted++
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	advanceCursor(cursor, ref, events)
	return true, nil
}

// advanceCursor moves the cursor to the last record this sweep read.
//
// It uses the events actually decoded rather than the manifest's numbers, so a manifest that runs
// ahead of the log -- a copied session directory, an interrupted sync -- cannot advance the cursor
// past records that were never read.
func advanceCursor(cursor *Cursor, ref SessionRef, events []Event) {
	if len(events) == 0 {
		return
	}
	last := events[len(events)-1]
	cursor.Generation = last.LogGeneration
	cursor.LastSeq = last.Seq
	if ref.Manifest != nil {
		cursor.UpdatedAtMS = ref.Manifest.UpdatedAtMS
	} else {
		cursor.UpdatedAtMS = ref.ModTimeUnixMS
	}
	// A session whose start was collected before this cursor existed still counts as started: the
	// records are in the log, and re-reporting the start on the next generation would double it.
	for i := range events {
		if events[i].Kind == KindSessionStarted && events[i].Seq <= cursor.LastSeq {
			cursor.Started = true
			break
		}
	}
}

func generationOf(events []Event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].LogGeneration
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
