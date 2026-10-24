// Package sessionsync holds the sync machinery every session-store collector shares: its cursor
// file, where its events go, and the read-only open of a runtime's SQLite store. Each collector
// keeps only how it reads its runtime's store and maps the records to events.
package sessionsync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sqliteuri"

	_ "modernc.org/sqlite"
)

// StateFile describes one collector's cursor file.
type StateFile[S any] struct {
	// Name is the collector an unreadable file's error names. Empty returns the decode error as is.
	Name string
	// New returns the state an empty path, a missing file or a file Valid rejects starts from.
	New func() *S
	// Valid reports whether a decoded file can be used, as a matching version does. Nil accepts any.
	Valid func(*S) bool
	// Stamp runs before each save, to record the version.
	Stamp func(*S)
	// BlankPath treats a path of only whitespace as empty.
	BlankPath bool
	// DirMode is the mode the state directory is created with; zero means 0o755.
	DirMode os.FileMode
}

func (f StateFile[S]) empty(path string) bool {
	if f.BlankPath {
		return strings.TrimSpace(path) == ""
	}
	return path == ""
}

// Load reads the cursor file at path. An empty path or a missing file yields a new state, and so
// does a file Valid rejects, which resets the collector to read its store from the start.
func (f StateFile[S]) Load(path string) (*S, error) {
	if f.empty(path) {
		return f.New(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f.New(), nil
		}
		return nil, err
	}
	stored := new(S)
	if err := json.Unmarshal(data, stored); err != nil {
		if f.Name == "" {
			return nil, err
		}
		return nil, fmt.Errorf("read %s collector state %s: %w", f.Name, path, err)
	}
	if f.Valid != nil && !f.Valid(stored) {
		return f.New(), nil
	}
	return stored, nil
}

// Save writes s to path through a temporary file and a rename, so a reader never sees half of
// it. An empty path saves nothing.
func (f StateFile[S]) Save(path string, s *S) error {
	if f.empty(path) {
		return nil
	}
	mode := f.DirMode
	if mode == 0 {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(path), mode); err != nil {
		return err
	}
	if f.Stamp != nil {
		f.Stamp(s)
	}
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

// Sink is where a sweep's events go: one JSON object per line to Out for --print, or appended to
// the runtime log with Log.
type Sink struct {
	Print bool
	// Out receives printed events; nil drops them.
	Out   io.Writer
	Write bool
	Log   writer.Options
}

// Emit prints or appends one event. A printing sweep never writes. An append refused by
// Log.Budget or Log.Guard returns writer.ErrBudgetSpent or writer.ErrRetentionWindowFull, which
// the sweep stops on.
func (s Sink) Emit(event schema.Event) error {
	if s.Print {
		if s.Out == nil {
			return nil
		}
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = s.Out.Write(append(data, '\n'))
		return err
	}
	if !s.Write {
		return nil
	}
	_, err := writer.AppendEvent(event, s.Log)
	return err
}

// GuardRetention gives a sweep that writes without printing a retention guard, so it stops with
// writer.ErrRetentionWindowFull rather than rotate its own output out of the log.
func (s *Sink) GuardRetention() {
	if g := NewRetentionGuard(s.Print, s.Write); g != nil {
		s.Log.Guard = g
	}
}

// NewRetentionGuard is the guard for one sweep that writes without printing, or nil for one that
// prints or does not write. A collector whose sink is built per event keeps it in its options for
// the whole sweep, since the guard tracks every file the sweep's appends landed in.
func NewRetentionGuard(print, write bool) *writer.RetentionGuard {
	if write && !print {
		return &writer.RetentionGuard{}
	}
	return nil
}

// Stopped reports whether err ends a sweep early without failing it: the budget ran out, or the
// retention guard refused an append that would have rotated the sweep's own output away. Either
// way nothing was written for the refused event, so a collector whose cursors move only past what
// was emitted leaves the rest for the next sweep.
func Stopped(err error) bool {
	return errors.Is(err, writer.ErrBudgetSpent) || errors.Is(err, writer.ErrRetentionWindowFull)
}

// Retention is the part of a sweep's summary that says how much of what it wrote the runtime log
// still holds. Collectors embed it, so the fields sit beside their own in --json output.
type Retention struct {
	// EventsRetained is how many of the events this sweep wrote are still in the runtime log or
	// its archives when it finishes. Events the writer suppressed as duplicates are in neither.
	EventsRetained int `json:"events_retained"`
	// EventsRotatedOut is how many events this sweep wrote that another writer's rotations
	// discarded before it finished. The sweep never rotates out its own output.
	EventsRotatedOut int `json:"events_rotated_out"`
	// RetentionLimited is set when the sweep stopped because writing more would have rotated its
	// own output out of the log. The sessions it had not finished keep their cursors.
	RetentionLimited bool `json:"retention_limited"`
}

// Finish records how the guarded sweep through s ended. stopErr is the error that stopped it
// early, if any. It returns an error when another writer rotated out some of what it wrote.
func (r *Retention) Finish(s Sink, stopErr error) error {
	if errors.Is(stopErr, writer.ErrRetentionWindowFull) {
		r.RetentionLimited = true
	}
	var err error
	r.EventsRetained, r.EventsRotatedOut, err = s.Retention()
	return err
}

// Retention reports how many of the events the guarded sweep wrote are still in the runtime log or
// its archives, and how many another writer's rotations discarded, with an error when any were.
// Without a guard it reports nothing.
func (s Sink) Retention() (retained, rotatedOut int, err error) {
	g := s.Log.Guard
	if g == nil {
		return 0, 0, nil
	}
	retained = g.Retained()
	rotatedOut = g.Written() - retained
	if rotatedOut > 0 {
		err = fmt.Errorf("%d of the %d events this sweep wrote were rotated out of the runtime log by another writer before it finished",
			rotatedOut, g.Written())
	}
	return retained, rotatedOut, err
}

// OpenSQLite opens a runtime's SQLite store read-only, through the snapshot a confined read takes,
// with pragmas passed as _pragma. modernc.org/sqlite opens every database READWRITE|CREATE, so
// mode=ro in the URI is the one thing keeping the read from writing to it.
func OpenSQLite(path string, pragmas ...string) (*sql.DB, error) {
	path, err := sessionfs.SQLitePath(path)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", sqliteuri.File(path, url.Values{"mode": {"ro"}, "_pragma": pragmas}))
}
