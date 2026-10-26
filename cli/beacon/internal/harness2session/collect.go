package harness2session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionwindow"
)

const StateVersion = 1

// Cursor records how far Beacon has read one trajectory file.
type Cursor struct {
	// SessionID is the session the file held when it was last read. Harness² re-rolls a run whose
	// output it cannot reuse by deleting the run directory and running again into the same path, so
	// a file that now names another session is a new stream, read from its start.
	SessionID string `json:"session_id,omitempty"`
	// SizeBytes and ModTimeUnixMS are the file as last read, a cheap check that skips files that
	// cannot have changed. They are not the record of what was written.
	SizeBytes     int64 `json:"size_bytes,omitempty"`
	ModTimeUnixMS int64 `json:"mod_time_unix_ms,omitempty"`
	// Line is the last line whose events were all written.
	Line int `json:"line,omitempty"`
	// Started records that session.started was written for this file.
	Started bool `json:"started,omitempty"`
	// Events is the running total written for this file, across all sweeps.
	Events int `json:"events,omitempty"`
}

type State struct {
	Version int `json:"version"`
	// Roots are the runs directories earlier sweeps were pointed at with --runs-dir. A sweep with no
	// directory of its own reads these as well as the default, so a scheduled sync and the
	// install-time backfill keep reading a checkout that lives somewhere Beacon would not look.
	Roots []string `json:"roots,omitempty"`
	// Files is keyed by the trajectory's absolute path.
	Files map[string]*Cursor `json:"files"`
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "harness2.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Harness²",
	New:   func() *State { return &State{Version: StateVersion, Files: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Files != nil },
	Stamp: func(s *State) {
		if s.Files == nil {
			s.Files = map[string]*Cursor{}
		}
		s.Version = StateVersion
	},
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

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

// remember adds roots to the ones later sweeps read.
func (s *State) remember(roots []string) {
	seen := map[string]bool{}
	for _, root := range s.Roots {
		seen[root] = true
	}
	for _, root := range roots {
		if !seen[root] {
			seen[root] = true
			s.Roots = append(s.Roots, root)
		}
	}
	sort.Strings(s.Roots)
}

type CollectOptions struct {
	// RunsDirs are the runs directories to read. Empty reads DefaultRunsDirs and every directory an
	// earlier sweep was given; given, they are read and remembered for later sweeps.
	RunsDirs  []string
	StatePath string
	LogPath   string
	Write     bool
	UserMode  bool
	Print     bool
	Out       io.Writer
	// Since, when set, limits the sweep to trajectories modified at or after it, newest first. Older
	// ones are not read and their cursors do not move, so a later sync still collects them. The
	// install-time backfill sets it; an ordinary sync leaves it zero.
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
	Roots          []string `json:"roots"`
	Trajectories   int      `json:"trajectories"`
	FilesChanged   int      `json:"files_changed"`
	EventsEmitted  int      `json:"events_emitted"`
	Errors         int      `json:"errors"`
	MalformedLines int      `json:"malformed_lines"`
	// PartialFiles counts trajectories that ended in a line the executor was still writing. That
	// line is read on a later sweep.
	PartialFiles int `json:"partial_files"`
	sessionsync.Retention
}

// Roots resolves the runs directories a sweep with these options reads: the ones given, or else
// the defaults together with every directory an earlier sweep remembered.
func Roots(runsDirs []string, state *State) ([]string, error) {
	if len(runsDirs) > 0 {
		store, err := NewStore(runsDirs)
		if err != nil {
			return nil, err
		}
		return store.Roots, nil
	}
	var roots []string
	roots = append(roots, DefaultRunsDirs()...)
	if state != nil {
		roots = append(roots, state.Roots...)
	}
	store, err := NewStore(roots)
	if err != nil {
		return nil, err
	}
	return store.Roots, nil
}

func CollectOnce(opts CollectOptions) (summary Summary, err error) {
	if opts.Print && !opts.Write {
		opts.StatePath = ""
	}
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	roots, err := Roots(opts.RunsDirs, state)
	if err != nil {
		return summary, err
	}
	summary.Roots = roots
	store := &Store{Roots: roots}
	refs, err := store.List()
	if err != nil {
		return summary, err
	}
	if len(opts.RunsDirs) > 0 {
		state.remember(roots)
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Harness² collector state: %w", saveErr)
		}
	}()
	refs = sessionwindow.Recent(refs, opts.Since, func(r TrajectoryRef) int64 { return r.ModTimeUnixMS })
	summary.Trajectories = len(refs)

	var errs []error
	opts.guard = sessionsync.NewRetentionGuard(opts.Print, opts.Write)
	var stopErr error
	for _, ref := range refs {
		changed, collectErr := collectFile(store, ref, state, opts, &summary)
		if sessionsync.Stopped(collectErr) {
			if changed {
				summary.FilesChanged++
			}
			stopErr = collectErr
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Harness² trajectory %s: %w", ref.Path, collectErr))
			continue
		}
		if changed {
			summary.FilesChanged++
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

func collectFile(store *Store, ref TrajectoryRef, state *State, opts CollectOptions, summary *Summary) (bool, error) {
	cursor := state.cursor(ref.Path)
	if cursor.SizeBytes > 0 && cursor.SizeBytes == ref.SizeBytes && cursor.ModTimeUnixMS == ref.ModTimeUnixMS {
		return false, nil
	}
	trajectory, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if stats.PartialTail {
		summary.PartialFiles++
	}
	if err != nil {
		return false, err
	}
	sessionID := sessionIDOf(trajectory)
	// A file that now holds another session, or fewer lines than the cursor has passed, was
	// rewritten rather than appended to: Harness² re-rolled the run into the same path. Its lines
	// are read from the start. A new session id names new events, so nothing already written is
	// written again; a rewrite of the same session keeps session.started from being repeated.
	if cursor.SessionID != "" && cursor.SessionID != sessionID {
		*cursor = Cursor{}
	} else if trajectory.LineCount < cursor.Line {
		cursor.Line = 0
	}
	cursor.SessionID = sessionID
	mapped := MapTrajectory(trajectory, MapOptions{MinLine: cursor.Line, SkipStarted: cursor.Started})
	if len(mapped) == 0 {
		cursor.Line = maxInt(cursor.Line, trajectory.LineCount)
		// A file with nothing new is not a changed file, but its size is remembered so it is not
		// read again until it changes.
		cursor.SizeBytes, cursor.ModTimeUnixMS = ref.SizeBytes, ref.ModTimeUnixMS
		return false, nil
	}
	written := 0
	for _, item := range mapped {
		if err := opts.sink().Emit(item.Event); err != nil {
			commit(cursor, mapped, written, trajectory.LineCount)
			return written > 0, err
		}
		written++
		summary.EventsEmitted++
	}
	commit(cursor, mapped, written, trajectory.LineCount)
	cursor.SizeBytes, cursor.ModTimeUnixMS = ref.SizeBytes, ref.ModTimeUnixMS
	return true, nil
}

// commit advances the cursor over the lines whose events were all written. One line can map to
// several events -- an apply_patch touching three files is three -- so a line with an unwritten
// event left on it stays behind the cursor and is mapped again whole by the next sweep, whose ids
// match the ones already written. On a clean pass every complete line advances, including the ones
// that mapped to nothing.
func commit(cursor *Cursor, mapped []MappedEvent, written, lineCount int) {
	for _, item := range mapped[:written] {
		if item.Event.Event.Action == "session.started" {
			cursor.Started = true
		}
	}
	cursor.Events += written
	if written == len(mapped) {
		cursor.Line = maxInt(cursor.Line, lineCount)
		return
	}
	blocked := mapped[written].SourceLine
	for _, item := range mapped[:written] {
		if item.SourceLine < blocked && item.SourceLine > cursor.Line {
			cursor.Line = item.SourceLine
		}
	}
	// Lines between the last written event and the blocked one mapped to nothing; they are done too.
	if blocked-1 > cursor.Line {
		cursor.Line = blocked - 1
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget, Guard: o.guard}}
}
