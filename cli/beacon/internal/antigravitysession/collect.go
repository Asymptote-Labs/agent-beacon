package antigravitysession

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

// Cursor records how far Beacon has read one conversation.
//
// A transcript is appended to while the conversation runs and again when it is resumed, so the
// cursor keeps the last line written rather than a count: remembering only the modification time
// and an event total would make every sweep after the conversation grew re-map it from its first
// line and append a duplicate set to the runtime log.
type Cursor struct {
	// UpdatedAtMS is the transcript's last-seen modification time, used to skip conversations
	// that cannot have changed. It is a cheap pre-filter, not the record of what was written.
	UpdatedAtMS int64 `json:"updated_at_ms,omitempty"`
	// Events is the running total of events handled for this conversation across all sweeps:
	// written, or suppressed as already recorded by the live hooks.
	Events int `json:"events,omitempty"`
	// Line is the last transcript line whose events were all written, as a physical line number.
	Line int `json:"line,omitempty"`
	// Started records that session.started was written. It has no line of its own.
	Started bool `json:"started,omitempty"`
}

type State struct {
	Version  int                `json:"version"`
	Sessions map[string]*Cursor `json:"sessions"`
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "antigravity-sessions.json")
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Antigravity CLI session",
	New:   func() *State { return &State{Version: StateVersion, Sessions: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Sessions != nil },
	Stamp: func(s *State) {
		if s.Sessions == nil {
			s.Sessions = map[string]*Cursor{}
		}
		s.Version = StateVersion
	},
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

func (s *State) cursor(id string) *Cursor {
	if s.Sessions == nil {
		s.Sessions = map[string]*Cursor{}
	}
	c := s.Sessions[id]
	if c == nil {
		c = &Cursor{}
		s.Sessions[id] = c
	}
	return c
}

type CollectOptions struct {
	// BrainDir is Antigravity's brain directory; empty is DefaultBrainDir. The summaries database
	// is read from beside it.
	BrainDir  string
	StatePath string
	LogPath   string
	Write     bool
	UserMode  bool
	Print     bool
	Out       io.Writer
	// Since, when set, limits the sweep to conversations modified at or after it, newest first.
	// Older ones are not read and their cursors do not move, so a later sync still collects them.
	// The install-time backfill sets it; an ordinary sync leaves it zero.
	Since time.Time
	// Budget, when set, caps the bytes this sweep may append. The sweep stops at the first event
	// that does not fit and returns writer.ErrBudgetSpent, with cursors past only what was written.
	Budget *writer.Budget
	// RotateBytes is the size the runtime log rotates at while this sweep writes it; zero is
	// writer.DefaultRotateBytes.
	RotateBytes int64
	// LiveCoverage, when set, is what the live Antigravity hooks already recorded. The hook payload
	// carries prompts and tool calls but no assistant text, reasoning or token counts, so for a
	// covered conversation the sweep keeps what hooks cannot see, and every prompt they did not
	// record (see liveDuplicate).
	LiveCoverage sessionsync.LiveCoverage

	// guard keeps the sweep from rotating its own output out of the log; CollectOnce sets it.
	guard *writer.RetentionGuard
}

type SummaryResult struct {
	Sessions        int `json:"sessions"`
	SessionsChanged int `json:"sessions_changed"`
	EventsEmitted   int `json:"events_emitted"`
	Errors          int `json:"errors"`
	MalformedLines  int `json:"malformed_lines"`
	// EventsSuppressedLive counts events the sweep mapped but did not emit because live hook
	// coverage made them duplicates (see CollectOptions.LiveCoverage).
	EventsSuppressedLive int `json:"events_suppressed_live"`
	sessionsync.Retention
}

func CollectOnce(opts CollectOptions) (summary SummaryResult, err error) {
	if opts.Print && !opts.Write {
		opts.StatePath = ""
	}
	store := NewStore(opts.BrainDir)
	refs, listErr := store.List()
	if listErr != nil && len(refs) == 0 {
		return summary, listErr
	}
	refs = sessionwindow.Recent(refs, opts.Since, func(r SessionRef) int64 { return r.ModTimeUnixMS })
	summary.Sessions = len(refs)
	if len(refs) == 0 {
		return summary, listErr
	}
	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Antigravity CLI session collector state: %w", saveErr)
		}
	}()

	var errs []error
	if listErr != nil {
		summary.Errors++
		errs = append(errs, listErr)
	}
	opts.guard = sessionsync.NewRetentionGuard(opts.Print, opts.Write)
	var stopErr error
	for _, ref := range refs {
		changed, collectErr := collectSession(store, ref, state, opts, &summary)
		if sessionsync.Stopped(collectErr) {
			// Not a failed conversation: this one and the rest are left for a later sync, with
			// cursors that have not moved past anything that was not written.
			if changed {
				summary.SessionsChanged++
			}
			stopErr = collectErr
			errs = append(errs, collectErr)
			break
		}
		if collectErr != nil {
			summary.Errors++
			errs = append(errs, fmt.Errorf("Antigravity CLI conversation %s: %w", ref.ID, collectErr))
			continue
		}
		if changed {
			summary.SessionsChanged++
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

func collectSession(store *Store, ref SessionRef, state *State, opts CollectOptions, summary *SummaryResult) (bool, error) {
	cursor := state.cursor(ref.ID)
	if cursor.UpdatedAtMS >= ref.ModTimeUnixMS && cursor.Events > 0 {
		return false, nil
	}
	data, stats, err := store.Read(ref)
	summary.MalformedLines += stats.Malformed
	if err != nil {
		return false, err
	}
	// A transcript that came back shorter than the cursor was rewritten, not appended to, and every
	// line of the new one would sit at or below the cursor and be skipped forever. Reset the
	// position so the rewritten content is read; Started is left alone, so session.started is not
	// written twice. The comparison is against the lines the file held, not the steps that parsed,
	// so one malformed line is not read as a rewrite; and a file that yielded no lines at all --
	// absent, or truncated to nothing for a moment -- never resets a good cursor.
	minLine := cursor.Line
	if data.Lines > 0 && data.Lines < cursor.Line {
		minLine = 0
		cursor.Line = 0
	}
	mapped := MapSession(data, MapOptions{MinLine: minLine, SkipStarted: cursor.Started})
	if len(mapped) == 0 {
		cursor.UpdatedAtMS = ref.ModTimeUnixMS
		return false, nil
	}
	// written is how far through mapped the sweep got: an event suppressed as a live duplicate is
	// passed over exactly as if it had been written, so the cursor moves past it.
	written, recordedPromptLine := 0, 0
	for _, item := range mapped {
		if liveDuplicate(item, ref.ID, opts.LiveCoverage, &recordedPromptLine) {
			written++
			summary.EventsSuppressedLive++
			continue
		}
		if err := opts.sink().Emit(item.Event); err != nil {
			commit(cursor, mapped, written)
			return true, err
		}
		written++
		summary.EventsEmitted++
	}
	commit(cursor, mapped, written)
	cursor.UpdatedAtMS = ref.ModTimeUnixMS
	return true, nil
}

// liveDuplicate reports whether the live hooks already recorded item. Prompts are matched by
// content hash, not by time: hooks written by Beacon v1.3.60 record only a conversation's first
// prompt, and a PreInvocation hook that failed records none of its turn, so a prompt the hooks never
// saw is backfilled even in a covered conversation. A handoff link goes with the prompt it was read
// from, since the hook path records the two together; recordedPromptLine carries the line of the
// last prompt found recorded. Everything else is sessionsync.LiveCoverage.Suppresses.
func liveDuplicate(item MappedEvent, conversationID string, coverage sessionsync.LiveCoverage, recordedPromptLine *int) bool {
	switch item.Event.Event.Action {
	case "prompt.submitted":
		hash := ""
		if item.Event.Content != nil {
			hash = item.Event.Content.Hash
		}
		if !coverage.RecordedPrompt(conversationID, hash) {
			return false
		}
		*recordedPromptLine = item.SourceLine
		return true
	case "session.handoff":
		return item.SourceLine > 0 && item.SourceLine == *recordedPromptLine
	}
	return coverage.Suppresses(conversationID, item.Event.Event.Action, item.TimestampMS)
}

// commit advances the cursor over the events that were actually written.
//
// One step can produce several events -- a planner response yields reasoning, a message, one event
// per tool call and its token usage -- so a line counts as collected only once every event it
// produced has been written. A line with an unwritten event left on it stays behind the cursor, and
// the next sweep re-emits the whole line, under the same event ids, rather than dropping its tail.
func commit(cursor *Cursor, mapped []MappedEvent, written int) {
	blocked := -1
	for _, item := range mapped[written:] {
		if item.SourceKind == SourceTranscript && (blocked < 0 || item.SourceLine < blocked) {
			blocked = item.SourceLine
		}
	}
	for _, item := range mapped[:written] {
		if item.SourceKind == SourceSession {
			cursor.Started = true
			continue
		}
		if blocked >= 0 && item.SourceLine >= blocked {
			continue
		}
		if item.SourceLine > cursor.Line {
			cursor.Line = item.SourceLine
		}
	}
	cursor.Events += written
}

func (o CollectOptions) sink() sessionsync.Sink {
	return sessionsync.Sink{Print: o.Print, Out: o.Out, Write: o.Write, Log: writer.Options{Path: o.LogPath, UserMode: o.UserMode, RotateSize: o.RotateBytes, Budget: o.Budget, Guard: o.guard}}
}
