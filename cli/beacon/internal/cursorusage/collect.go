package cursorusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

// StateVersion is the on-disk version of the collector state. A file of another version is
// discarded: the cost is one lookback window fetched again and suppressed by the fresh state's
// first-run window, while misreading a cursor would skip or repeat spend.
const StateVersion = 1

const (
	// DefaultLookback is how far behind the last sync each sync re-reads. Cursor posts some usage
	// events after the request that caused them, so a window that started exactly at the last
	// sync's end would miss a late event for good. Everything re-read is suppressed by key.
	DefaultLookback = 48 * time.Hour
	// DefaultInitialWindow is how far back a first sync reads when no start is given.
	DefaultInitialWindow = 7 * 24 * time.Hour
	// MaxWindow is the longest window one query asks for. Longer ranges are split, so a backfill
	// over months is a series of ordinary requests rather than one the API may refuse.
	MaxWindow = 30 * 24 * time.Hour
)

// Cursor is what the collector remembers about one scope between syncs.
//
// CoveredFromMS and HighWaterMS bound the span already collected. A sync fetches only what falls
// before CoveredFromMS (a backfill reaching further back than any earlier sync) and the lookback
// window behind HighWaterMS, and writes what is not already in Seen; the span between the two is
// already in the log and is never fetched again (see segments). Seen holds only the keys inside the lookback window, so the file stays the
// size of two days of requests however long the collector has run.
type Cursor struct {
	CoveredFromMS int64 `json:"covered_from_ms,omitempty"`
	HighWaterMS   int64 `json:"high_water_ms,omitempty"`
	// SeenFromMS is where Seen starts being complete: every event collected at or after it has
	// its key in Seen. It is persisted rather than recomputed from the lookback, because a sync
	// run with a longer lookback than the last one would otherwise treat a span whose keys were
	// already pruned as never collected and write it again.
	SeenFromMS int64            `json:"seen_from_ms,omitempty"`
	Seen       map[string]int64 `json:"seen,omitempty"`
	LastSyncMS int64            `json:"last_sync_ms,omitempty"`
}

// State is the collector's file: one cursor per scope. A scope is the filter the sync ran with
// (one member's email, one user id, or the whole team), because a cursor earned for one member
// says nothing about what has been collected for another.
type State struct {
	Version int                `json:"version"`
	Scopes  map[string]*Cursor `json:"scopes"`
}

// ScopeKey names the cursor a query uses.
func ScopeKey(q Query) string {
	if email := strings.ToLower(strings.TrimSpace(q.Email)); email != "" {
		return "email:" + email
	}
	if id := strings.TrimSpace(q.UserID); id != "" {
		return "user:" + id
	}
	return "team"
}

// LoadState reads the state file. A missing file or an empty path is an empty state.
func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Scopes: map[string]*Cursor{}}
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
		return nil, fmt.Errorf("read Cursor usage collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Scopes == nil {
		return state, nil
	}
	state.Scopes = stored.Scopes
	return state, nil
}

// Save writes the state atomically. An empty path is a no-op.
func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

// Cursor returns the scope's cursor, creating it.
func (s *State) Cursor(scope string) *Cursor {
	if s.Scopes == nil {
		s.Scopes = map[string]*Cursor{}
	}
	c := s.Scopes[scope]
	if c == nil {
		c = &Cursor{}
		s.Scopes[scope] = c
	}
	if c.Seen == nil {
		c.Seen = map[string]int64{}
	}
	return c
}

// DefaultStatePath is where the collector keeps its state for the current user. It holds event
// fingerprints and timestamps, never the key.
func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "cursor-usage.json")
}

// Fetcher is the part of Client the collector uses.
type Fetcher interface {
	FetchAll(ctx context.Context, q Query) ([]UsageEvent, error)
}

// CollectOptions configures one sync.
type CollectOptions struct {
	Fetcher Fetcher
	// Query carries the member filter and page size. Its dates are ignored; the collector chooses
	// the window.
	Query Query
	// Since forces the window to start no later than this. Zero means: from the cursor, or
	// DefaultInitialWindow back on a first sync.
	Since time.Time
	// Lookback overrides DefaultLookback.
	Lookback time.Duration
	// Now overrides the clock.
	Now func() time.Time

	// StatePath is the state file. Empty disables persistence.
	StatePath string
	// Write appends events to the runtime log at LogPath.
	Write    bool
	LogPath  string
	UserMode bool
	// Print writes each event to Out as JSON.
	Print bool
	Out   io.Writer

	// emitHook replaces the writer in tests.
	emitHook func(schema.Event) error
}

// Summary reports what a sync did.
type Summary struct {
	Scope       string `json:"scope"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Fetched     int    `json:"fetched"`
	Emitted     int    `json:"emitted"`
	// Duplicates were already collected by an earlier sync.
	Duplicates int `json:"duplicates"`
	// NoUsage were requests with no tokens and no charge.
	NoUsage int `json:"no_usage"`
}

// Window returns the span a sync over the cursor would query.
func Window(c *Cursor, since, now time.Time, lookback time.Duration) (time.Time, time.Time) {
	if lookback <= 0 {
		lookback = DefaultLookback
	}
	var start time.Time
	if c != nil && c.HighWaterMS > 0 {
		start = time.UnixMilli(c.HighWaterMS).Add(-lookback)
	} else {
		start = now.Add(-DefaultInitialWindow)
	}
	if !since.IsZero() && (c == nil || c.HighWaterMS == 0 || since.Before(start)) {
		start = since
	}
	if start.After(now) {
		start = now
	}
	// Millisecond edges, because that is the API's resolution: a window edge between two
	// milliseconds would let adjacent chunks leave an event in neither.
	return start.UTC().Truncate(time.Millisecond), now.UTC().Truncate(time.Millisecond)
}

// CollectOnce runs one sync: fetch the window, drop what an earlier sync already wrote, write the
// rest oldest first, and advance the cursor.
//
// The cursor's high-water mark moves only after every event in the window was written. A sync
// that fails partway keeps the keys of the events it did write, so the retry skips them and
// writes the rest.
func CollectOnce(ctx context.Context, opts CollectOptions) (summary Summary, err error) {
	if opts.Fetcher == nil {
		return summary, errors.New("no Cursor usage fetcher")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	lookback := opts.Lookback
	if lookback <= 0 {
		lookback = DefaultLookback
	}

	state, err := LoadState(opts.StatePath)
	if err != nil {
		return summary, err
	}
	scope := ScopeKey(opts.Query)
	cursor := state.Cursor(scope)
	summary.Scope = scope

	end := now().UTC()
	start, end := Window(cursor, opts.Since, end, lookback)
	summary.WindowStart = start.Format(time.RFC3339)
	summary.WindowEnd = end.Format(time.RFC3339)

	persist := func() {
		if saveErr := state.Save(opts.StatePath); saveErr != nil && err == nil {
			err = fmt.Errorf("save Cursor usage collector state: %w", saveErr)
		}
	}

	var events []UsageEvent
	seenFrom := cursor.seenFrom(lookback)
	for _, segment := range segments(cursor, start, end, seenFrom) {
		got, fetchErr := fetchSegment(ctx, opts.Fetcher, opts.Query, segment[0], segment[1])
		if fetchErr != nil {
			// Nothing has been written yet, so nothing is lost by stopping here: the next sync
			// asks for the same window.
			return summary, fetchErr
		}
		events = append(events, got...)
	}
	events = inWindow(events, start, end)
	summary.Fetched = len(events)

	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].Timestamp.Before(events[j].Timestamp)
		}
		return Fingerprint(events[i]) < Fingerprint(events[j])
	})
	keys := DedupKeys(events)

	for i, ev := range events {
		ts := ev.Timestamp.UnixMilli()
		key := keys[i]
		if _, ok := cursor.Seen[key]; ok {
			summary.Duplicates++
			continue
		}
		if !HasUsage(ev) {
			summary.NoUsage++
			if !opts.Print {
				cursor.Seen[key] = ts
			}
			continue
		}
		if emitErr := emit(MapEvent(ev), opts); emitErr != nil {
			persist()
			return summary, emitErr
		}
		summary.Emitted++
		if !opts.Print {
			cursor.Seen[key] = ts
		}
	}

	if opts.Print {
		return summary, nil
	}
	if cursor.CoveredFromMS == 0 || start.UnixMilli() < cursor.CoveredFromMS {
		cursor.CoveredFromMS = start.UnixMilli()
	}
	if end.UnixMilli() > cursor.HighWaterMS {
		cursor.HighWaterMS = end.UnixMilli()
	}
	cursor.LastSyncMS = end.UnixMilli()
	prune := time.UnixMilli(cursor.HighWaterMS).Add(-lookback).UnixMilli()
	// The completeness boundary never moves back: keys behind it are already gone.
	if prune < cursor.SeenFromMS {
		prune = cursor.SeenFromMS
	}
	for key, ts := range cursor.Seen {
		if ts < prune {
			delete(cursor.Seen, key)
		}
	}
	cursor.SeenFromMS = prune
	persist()
	return summary, err
}

// segments lists the spans a sync has to fetch. Normally that is the whole window. A backfill
// that reaches before anything collected so far skips the span earlier syncs already read
// completely: fetching it again would only produce events the cursor then discards.
func segments(c *Cursor, start, end, seenFrom time.Time) [][2]time.Time {
	if c == nil || c.HighWaterMS == 0 || c.CoveredFromMS == 0 {
		return [][2]time.Time{{start, end}}
	}
	covered := time.UnixMilli(c.CoveredFromMS).UTC()
	recent := seenFrom
	if recent.Before(covered) {
		// Seen reaches past everything collected: there is no fully read middle span.
		recent = covered
	}
	tail := recent
	if tail.Before(start) {
		tail = start
	}
	if !start.Before(covered) {
		return [][2]time.Time{{tail, end}}
	}
	return [][2]time.Time{
		{start, covered.Add(-time.Millisecond)},
		{tail, end},
	}
}

// seenFrom is where the cursor's Seen set is complete. A state written before SeenFromMS existed
// falls back to the lookback behind the high-water mark, which is what it pruned to.
func (c *Cursor) seenFrom(lookback time.Duration) time.Time {
	if c.SeenFromMS > 0 {
		return time.UnixMilli(c.SeenFromMS).UTC()
	}
	if c.HighWaterMS > 0 {
		return time.UnixMilli(c.HighWaterMS).Add(-lookback).UTC()
	}
	return time.Time{}
}

// fetchSegment fetches [start, end] in pieces no longer than MaxWindow. The pieces do not share a
// millisecond: the API's end date is inclusive, and an event on a shared boundary would come back
// in both pieces and read as two requests.
func fetchSegment(ctx context.Context, f Fetcher, base Query, start, end time.Time) ([]UsageEvent, error) {
	var out []UsageEvent
	for chunkStart := start; !chunkStart.After(end); {
		chunkEnd := chunkStart.Add(MaxWindow)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		q := base
		q.StartDate = chunkStart
		q.EndDate = chunkEnd
		got, err := f.FetchAll(ctx, q)
		if err != nil {
			return out, err
		}
		out = append(out, inWindow(got, chunkStart, chunkEnd)...)
		chunkStart = chunkEnd.Add(time.Millisecond)
	}
	return out, nil
}

// inWindow drops events outside [start, end]. A host that ignores the requested dates would
// otherwise put events outside the window the cursor accounts for.
func inWindow(events []UsageEvent, start, end time.Time) []UsageEvent {
	out := events[:0]
	for _, ev := range events {
		if ev.Timestamp.Before(start) || ev.Timestamp.After(end) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func emit(event schema.Event, opts CollectOptions) error {
	if opts.emitHook != nil {
		return opts.emitHook(event)
	}
	if opts.Print && opts.Out != nil {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := opts.Out.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if opts.Write && !opts.Print {
		if _, err := writer.AppendEvent(event, writer.Options{Path: opts.LogPath, UserMode: opts.UserMode}); err != nil {
			return err
		}
	}
	return nil
}
