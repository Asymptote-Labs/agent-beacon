package cursorusage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// fakeFetcher serves a fixed set of events the way the API does: everything whose timestamp falls
// inside the inclusive [startDate, endDate] window of the query.
type fakeFetcher struct {
	events  []UsageEvent
	queries []Query
	err     error
	// ignoreDates makes the fake return every event regardless of the window.
	ignoreDates bool
}

func (f *fakeFetcher) FetchAll(_ context.Context, q Query) ([]UsageEvent, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	var out []UsageEvent
	for _, ev := range f.events {
		if f.ignoreDates || (!ev.Timestamp.Before(q.StartDate) && !ev.Timestamp.After(q.EndDate)) {
			out = append(out, ev)
		}
	}
	return out, nil
}

var baseNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

func usageAt(ts time.Time, input int64) UsageEvent {
	return UsageEvent{
		Timestamp:    ts,
		Model:        "claude-4.5-sonnet",
		Kind:         "USAGE_EVENT_KIND_USAGE_BASED",
		TokenBased:   true,
		TokenUsage:   TokenUsage{InputTokens: input, OutputTokens: 10, CacheReadTokens: 100, CacheWriteTokens: 5, TotalCents: f64(1.5)},
		ChargedCents: f64(2),
		UserEmail:    "dev@example.com",
		UserID:       "42",
	}
}

type harness struct {
	t        *testing.T
	fetcher  *fakeFetcher
	now      time.Time
	state    string
	log      string
	query    Query
	since    time.Time
	lookback time.Duration
}

func newHarness(t *testing.T, events ...UsageEvent) *harness {
	dir := t.TempDir()
	return &harness{
		t:       t,
		fetcher: &fakeFetcher{events: events},
		now:     baseNow,
		state:   filepath.Join(dir, "state", "cursor-usage.json"),
		log:     filepath.Join(dir, "logs", "runtime.jsonl"),
		query:   Query{Email: "dev@example.com"},
	}
}

func (h *harness) sync() Summary {
	h.t.Helper()
	summary, err := CollectOnce(context.Background(), h.opts())
	if err != nil {
		h.t.Fatal(err)
	}
	return summary
}

func (h *harness) opts() CollectOptions {
	return CollectOptions{
		Fetcher:   h.fetcher,
		Query:     h.query,
		Since:     h.since,
		Lookback:  h.lookback,
		Now:       func() time.Time { return h.now },
		StatePath: h.state,
		Write:     true,
		LogPath:   h.log,
	}
}

func (h *harness) logged() []schema.Event {
	h.t.Helper()
	f, err := os.Open(h.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			h.t.Fatalf("log line is not an event: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func TestMapEvent(t *testing.T) {
	ev := usageAt(time.UnixMilli(1750979225854).UTC(), 126)
	ev.CursorTokenFeeCents = f64(0.25)
	ev.Chargeable = new(bool)
	*ev.Chargeable = true
	ev.MaxMode = true
	out := MapEvent(ev)

	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
	if out.Event.Action != "token.usage" || out.Event.Category != "metric" || out.Event.Fidelity != schema.FidelityObserved {
		t.Errorf("event = %+v", out.Event)
	}
	if out.Harness.Name != "cursor" || out.Harness.CollectionMethod != schema.CollectionMethodPoll {
		t.Errorf("harness = %+v", out.Harness)
	}
	if out.Session != nil {
		t.Errorf("Admin API events name no conversation; session must be unset, got %+v", out.Session)
	}
	if out.Timestamp != schema.FormatTimestamp(ev.Timestamp) || out.Model != "claude-4.5-sonnet" {
		t.Errorf("timestamp/model = %s %s", out.Timestamp, out.Model)
	}
	u := out.GenAI.Usage
	if *u.InputTokens != 126 || *u.OutputTokens != 10 || *u.CacheRead.InputTokens != 100 || *u.CacheCreation.InputTokens != 5 {
		t.Errorf("usage = %+v", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.02 {
		t.Errorf("cost = %v, want chargedCents/100", u.CostUSD)
	}
	raw := out.Raw["cursor"].(map[string]interface{})
	for key, want := range map[string]interface{}{
		"source": "admin_api", "token_cents": 1.5, "charged_cents": 2.0, "cursor_token_fee_cents": 0.25,
		"chargeable": true, "max_mode": true, "user_email": "dev@example.com", "user_id": "42",
	} {
		if raw[key] != want {
			t.Errorf("raw.cursor.%s = %v, want %v", key, raw[key], want)
		}
	}
}

func TestMapEventLeavesAbsentValuesAbsent(t *testing.T) {
	out := MapEvent(UsageEvent{Timestamp: baseNow, Model: "auto", TokenUsage: TokenUsage{OutputTokens: 3}})
	u := out.GenAI.Usage
	if u.InputTokens != nil || u.CacheRead != nil || u.CacheCreation != nil || u.CostUSD != nil {
		t.Errorf("zero counts and an absent charge must not be written: %+v", u)
	}
	if *u.OutputTokens != 3 {
		t.Errorf("output = %v", u.OutputTokens)
	}
}

func TestHasUsage(t *testing.T) {
	if HasUsage(UsageEvent{}) {
		t.Error("an empty event has no usage")
	}
	if HasUsage(UsageEvent{ChargedCents: f64(0), RequestsCosts: f64(1)}) {
		t.Error("a request-billed call with a zero charge has nothing to sum")
	}
	if !HasUsage(UsageEvent{ChargedCents: f64(4)}) {
		t.Error("a charge alone is usage")
	}
	if !HasUsage(UsageEvent{TokenUsage: TokenUsage{CacheReadTokens: 1}}) {
		t.Error("cache tokens alone are usage")
	}
}

func TestDedupKeysSeparateIdenticalRequests(t *testing.T) {
	a := usageAt(baseNow, 1)
	b := usageAt(baseNow, 2)
	keys := DedupKeys([]UsageEvent{a, b, a})
	if keys[0] == keys[2] {
		t.Fatal("two identical requests must get two keys")
	}
	if keys[0] == keys[1] {
		t.Fatal("different requests must get different keys")
	}
	// Order among non-identical events does not change any key.
	again := DedupKeys([]UsageEvent{b, a, a})
	if again[0] != keys[1] || again[1] != keys[0] || again[2] != keys[2] {
		t.Fatalf("keys depend on order: %v vs %v", keys, again)
	}
}

func TestFingerprintDistinguishesAbsentFromZeroCharge(t *testing.T) {
	a := usageAt(baseNow, 1)
	b := a
	b.ChargedCents = nil
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("an absent charge and a charge must fingerprint differently")
	}
}

func TestFirstSyncWritesTheInitialWindowAndARepeatWritesNothing(t *testing.T) {
	h := newHarness(t,
		usageAt(baseNow.Add(-8*24*time.Hour), 999), // before the default first window
		usageAt(baseNow.Add(-6*24*time.Hour), 1),
		usageAt(baseNow.Add(-time.Hour), 2),
		UsageEvent{Timestamp: baseNow.Add(-2 * time.Hour), Model: "auto", RequestsCosts: f64(1)},
	)
	first := h.sync()
	if first.Fetched != 3 || first.Emitted != 2 || first.NoUsage != 1 || first.Duplicates != 0 {
		t.Fatalf("first = %+v", first)
	}
	if first.Scope != "email:dev@example.com" {
		t.Fatalf("scope = %q", first.Scope)
	}
	logged := h.logged()
	if len(logged) != 2 {
		t.Fatalf("logged %d events", len(logged))
	}
	// Oldest first.
	if *logged[0].GenAI.Usage.InputTokens != 1 || *logged[1].GenAI.Usage.InputTokens != 2 {
		t.Fatalf("order = %v, %v", *logged[0].GenAI.Usage.InputTokens, *logged[1].GenAI.Usage.InputTokens)
	}
	for _, ev := range logged {
		if ev.Event.ID == "" {
			t.Error("the writer must stamp event.id")
		}
	}

	h.now = h.now.Add(time.Minute)
	second := h.sync()
	if second.Emitted != 0 {
		t.Fatalf("a repeat sync wrote %d events: %+v", second.Emitted, second)
	}
	if len(h.logged()) != 2 {
		t.Fatal("the log grew on a repeat sync")
	}
	// The repeat asks only for the lookback window, not the whole first window again.
	q := h.fetcher.queries[len(h.fetcher.queries)-1]
	if got := baseNow.Sub(q.StartDate); got != DefaultLookback {
		t.Fatalf("repeat window starts %v before the last sync, want %v", got, DefaultLookback)
	}
}

func TestLateEventInsideTheLookbackIsCollected(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-time.Hour), 1))
	h.sync()
	// Cursor posts an event for a request made before the last sync, after it ran.
	h.fetcher.events = append(h.fetcher.events, usageAt(baseNow.Add(-30*time.Minute), 2))
	h.now = baseNow.Add(10 * time.Minute)
	summary := h.sync()
	if summary.Emitted != 1 || summary.Duplicates != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(h.logged()) != 2 {
		t.Fatal("late event not written")
	}
}

func TestIdenticalRequestsAreBothWrittenOnceEach(t *testing.T) {
	ev := usageAt(baseNow.Add(-time.Hour), 7)
	h := newHarness(t, ev, ev)
	if s := h.sync(); s.Emitted != 2 {
		t.Fatalf("summary = %+v", s)
	}
	h.now = baseNow.Add(time.Minute)
	if s := h.sync(); s.Emitted != 0 || s.Duplicates != 2 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestBackfillReachesFurtherBackWithoutRepeatingCollectedSpend(t *testing.T) {
	h := newHarness(t,
		usageAt(baseNow.Add(-20*24*time.Hour), 1),
		usageAt(baseNow.Add(-5*24*time.Hour), 2), // collected by the first sync, pruned from Seen later
		usageAt(baseNow.Add(-time.Hour), 3),
	)
	h.sync()
	// Several days pass with syncs, so the -5d event leaves the Seen window.
	for i := 1; i <= 6; i++ {
		h.now = baseNow.Add(time.Duration(i) * 24 * time.Hour)
		h.sync()
	}
	if len(h.logged()) != 2 {
		t.Fatalf("logged = %d", len(h.logged()))
	}

	h.fetcher.queries = nil
	h.since = baseNow.Add(-30 * 24 * time.Hour)
	summary := h.sync()
	if summary.Emitted != 1 {
		t.Fatalf("backfill wrote %d events, want only the one older than anything collected: %+v", summary.Emitted, summary)
	}
	if got := len(h.logged()); got != 3 {
		t.Fatalf("logged = %d, want 3", got)
	}
	// The span already collected is not fetched again.
	covered := baseNow.Add(-DefaultInitialWindow)
	for _, q := range h.fetcher.queries {
		if q.StartDate.Before(covered) && q.EndDate.After(covered.Add(time.Hour)) {
			t.Fatalf("query %v..%v re-reads the collected span", q.StartDate, q.EndDate)
		}
	}

	// And running the same backfill again writes nothing and fetches only the recent window.
	h.fetcher.queries = nil
	h.now = h.now.Add(time.Minute)
	if s := h.sync(); s.Emitted != 0 {
		t.Fatalf("second backfill wrote %d", s.Emitted)
	}
	for _, q := range h.fetcher.queries {
		if q.StartDate.Before(h.now.Add(-DefaultLookback - time.Hour)) {
			t.Fatalf("repeat backfill re-read %v..%v", q.StartDate, q.EndDate)
		}
	}
}

// A host that returns the collected span although it was not asked for it must not get it
// written twice: the span is never requested, and anything outside a request is dropped.
func TestCollectedSpanIsSkippedEvenWhenTheHostReturnsIt(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-5*24*time.Hour), 1), usageAt(baseNow.Add(-time.Hour), 2))
	h.sync()
	for i := 1; i <= 6; i++ {
		h.now = baseNow.Add(time.Duration(i) * 24 * time.Hour)
		h.sync()
	}
	h.fetcher.ignoreDates = true
	h.lookback = 30 * 24 * time.Hour // fetch window now covers the -5d event again
	h.now = h.now.Add(time.Minute)
	s := h.sync()
	if s.Emitted != 0 {
		t.Fatalf("re-wrote %d collected events: %+v", s.Emitted, s)
	}
}

func TestSegments(t *testing.T) {
	ms := func(d time.Duration) time.Time { return baseNow.Add(d).Truncate(time.Millisecond) }
	c := &Cursor{CoveredFromMS: ms(-10 * 24 * time.Hour).UnixMilli(), HighWaterMS: baseNow.UnixMilli()}
	seen := ms(-48 * time.Hour)

	got := segments(nil, ms(-time.Hour), baseNow, time.Time{})
	if len(got) != 1 || !got[0][0].Equal(ms(-time.Hour)) {
		t.Fatalf("no cursor = %v", got)
	}
	got = segments(c, seen, baseNow, seen)
	if len(got) != 1 || !got[0][0].Equal(seen) {
		t.Fatalf("ordinary resume = %v", got)
	}
	got = segments(c, ms(-10*24*time.Hour), baseNow, seen)
	if len(got) != 1 || !got[0][0].Equal(seen) {
		t.Fatalf("since at the covered edge must fetch only the recent window: %v", got)
	}
	got = segments(c, ms(-20*24*time.Hour), baseNow, seen)
	if len(got) != 2 || !got[0][1].Equal(ms(-10*24*time.Hour).Add(-time.Millisecond)) || !got[1][0].Equal(seen) {
		t.Fatalf("backfill = %v", got)
	}
	got = segments(c, ms(-20*24*time.Hour), baseNow, ms(-15*24*time.Hour))
	if len(got) != 2 || !got[1][0].Equal(ms(-10*24*time.Hour)) {
		t.Fatalf("seen reaching past the covered edge = %v", got)
	}
}

func TestLongerLookbackDoesNotRewriteCollectedSpend(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-5*24*time.Hour), 1), usageAt(baseNow.Add(-time.Hour), 2))
	h.sync()
	for i := 1; i <= 6; i++ {
		h.now = baseNow.Add(time.Duration(i) * 24 * time.Hour)
		h.sync()
	}
	h.lookback = 30 * 24 * time.Hour
	h.now = h.now.Add(time.Minute)
	if s := h.sync(); s.Emitted != 0 {
		t.Fatalf("a longer lookback re-wrote %d events: %+v", s.Emitted, s)
	}
	// And a later sync with the same longer lookback: the completeness boundary must not have
	// moved back over keys that were already pruned.
	h.now = h.now.Add(time.Hour)
	if s := h.sync(); s.Emitted != 0 {
		t.Fatalf("the next sync with the longer lookback re-wrote %d events: %+v", s.Emitted, s)
	}
	if len(h.logged()) != 2 {
		t.Fatalf("logged = %d", len(h.logged()))
	}
}

func TestLongWindowsAreSplitWithoutSharedBoundaries(t *testing.T) {
	h := newHarness(t)
	h.since = baseNow.Add(-75 * 24 * time.Hour)
	boundary := h.since.Add(MaxWindow)
	h.fetcher.events = []UsageEvent{usageAt(boundary, 1), usageAt(boundary.Add(time.Millisecond), 2)}
	summary := h.sync()
	if len(h.fetcher.queries) != 3 {
		t.Fatalf("queries = %d, want 3 for 75 days", len(h.fetcher.queries))
	}
	for i := 1; i < len(h.fetcher.queries); i++ {
		prev, cur := h.fetcher.queries[i-1], h.fetcher.queries[i]
		if cur.StartDate.Sub(prev.EndDate) != time.Millisecond {
			t.Fatalf("chunks %d and %d are not adjacent: %v .. %v", i-1, i, prev.EndDate, cur.StartDate)
		}
		if cur.EndDate.Sub(cur.StartDate) > MaxWindow {
			t.Fatalf("chunk %d is longer than MaxWindow", i)
		}
	}
	if summary.Emitted != 2 || len(h.logged()) != 2 {
		t.Fatalf("an event on a chunk boundary must be written once: %+v", summary)
	}
}

func TestEventsOutsideTheWindowAreDropped(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-90*24*time.Hour), 1), usageAt(baseNow.Add(time.Hour), 2), usageAt(baseNow.Add(-time.Hour), 3))
	h.fetcher.ignoreDates = true
	if s := h.sync(); s.Emitted != 1 || s.Fetched != 1 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFetchErrorWritesNothingAndKeepsTheCursor(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-time.Hour), 1))
	h.fetcher.err = errors.New("boom")
	if _, err := CollectOnce(context.Background(), h.opts()); err == nil {
		t.Fatal("expected the fetch error")
	}
	if len(h.logged()) != 0 {
		t.Fatal("wrote events after a failed fetch")
	}
	if _, err := os.Stat(h.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed fetch must not create state")
	}
	h.fetcher.err = nil
	if s := h.sync(); s.Emitted != 1 {
		t.Fatalf("retry = %+v", s)
	}
}

func TestPartialWriteFailureResumesWithoutDuplicates(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-3*time.Hour), 1), usageAt(baseNow.Add(-2*time.Hour), 2), usageAt(baseNow.Add(-time.Hour), 3))
	var written []int64
	opts := h.opts()
	opts.emitHook = func(ev schema.Event) error {
		if len(written) == 2 {
			return errors.New("disk full")
		}
		written = append(written, *ev.GenAI.Usage.InputTokens)
		return nil
	}
	if _, err := CollectOnce(context.Background(), opts); err == nil {
		t.Fatal("expected the write error")
	}
	state, err := LoadState(h.state)
	if err != nil {
		t.Fatal(err)
	}
	cursor := state.Scopes["email:dev@example.com"]
	if cursor == nil || len(cursor.Seen) != 2 || cursor.HighWaterMS != 0 {
		t.Fatalf("after a partial write the cursor must hold the written keys and not advance: %+v", cursor)
	}

	opts.emitHook = func(ev schema.Event) error {
		written = append(written, *ev.GenAI.Usage.InputTokens)
		return nil
	}
	if _, err := CollectOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 || written[0] != 1 || written[1] != 2 || written[2] != 3 {
		t.Fatalf("written = %v, want each event exactly once", written)
	}
}

func TestScopesKeepSeparateCursors(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-time.Hour), 1))
	h.sync()
	h.query = Query{Email: "other@example.com"}
	if s := h.sync(); s.Emitted != 1 || s.Scope != "email:other@example.com" {
		t.Fatalf("a different member starts from its own first window: %+v", s)
	}
	h.query = Query{}
	if s := h.sync(); s.Scope != "team" || s.Emitted != 1 {
		t.Fatalf("team scope = %+v", s)
	}
	if got := ScopeKey(Query{UserID: " 42 "}); got != "user:42" {
		t.Fatalf("user scope = %q", got)
	}
	if got := ScopeKey(Query{Email: "Dev@Example.com", UserID: "42"}); got != "email:dev@example.com" {
		t.Fatalf("email wins and is case-folded: %q", got)
	}
}

func TestPrintModeWritesNoLogAndNoState(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-time.Hour), 1))
	var out bytes.Buffer
	opts := h.opts()
	opts.Print = true
	opts.Write = false
	opts.StatePath = ""
	opts.Out = &out
	for i := 0; i < 2; i++ {
		s, err := CollectOnce(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if s.Emitted != 1 {
			t.Fatalf("print run %d emitted %d", i, s.Emitted)
		}
	}
	if len(h.logged()) != 0 {
		t.Fatal("print wrote the log")
	}
	if lines := bytes.Count(out.Bytes(), []byte("\n")); lines != 2 {
		t.Fatalf("printed %d lines", lines)
	}
}

func TestSeenIsPrunedToTheLookbackWindow(t *testing.T) {
	var events []UsageEvent
	for i := 0; i < 24*10; i++ {
		events = append(events, usageAt(baseNow.Add(-time.Duration(i)*time.Hour), int64(i+1)))
	}
	h := newHarness(t, events...)
	h.since = baseNow.Add(-11 * 24 * time.Hour)
	h.sync()
	state, err := LoadState(h.state)
	if err != nil {
		t.Fatal(err)
	}
	cursor := state.Scopes["email:dev@example.com"]
	if len(cursor.Seen) > 49 {
		t.Fatalf("seen holds %d keys, want only the 48h lookback", len(cursor.Seen))
	}
	if cursor.SeenFromMS != baseNow.Add(-DefaultLookback).UnixMilli() {
		t.Fatalf("seen_from_ms = %d", cursor.SeenFromMS)
	}
	if info, err := os.Stat(h.state); err == nil && info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("state mode = %v", info.Mode().Perm())
	}
}

func TestLoadStateResetsOtherVersionsAndRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"scopes":{"team":{"high_water_ms":5}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil || len(state.Scopes) != 0 {
		t.Fatalf("other version = %+v, %v", state, err)
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("garbage state must be an error, not a silent reset")
	}
}

func TestWindow(t *testing.T) {
	now := baseNow.Add(123456 * time.Nanosecond)
	start, end := Window(nil, time.Time{}, now, 0)
	if !start.Equal(baseNow.Add(-DefaultInitialWindow)) || !end.Equal(baseNow) {
		t.Fatalf("first window = %v..%v (edges must be whole milliseconds)", start, end)
	}
	c := &Cursor{HighWaterMS: baseNow.UnixMilli()}
	start, _ = Window(c, time.Time{}, now, time.Hour)
	if !start.Equal(baseNow.Add(-time.Hour)) {
		t.Fatalf("resumed window starts %v", start)
	}
	start, _ = Window(c, baseNow.Add(-time.Minute), now, time.Hour)
	if !start.Equal(baseNow.Add(-time.Hour)) {
		t.Fatalf("since narrowed the window: %v", start)
	}
	start, end = Window(nil, baseNow.Add(time.Hour), now, 0)
	if !start.Equal(end) {
		t.Fatalf("future since = %v..%v", start, end)
	}
}

// The end-to-end check: what the collector writes is what `beacon token-usage` and the dashboard
// token view sum.
func TestCollectedEventsRollUpInTheTokenReport(t *testing.T) {
	h := newHarness(t, usageAt(baseNow.Add(-2*time.Hour), 100), usageAt(baseNow.Add(-time.Hour), 200))
	h.sync()
	report := tokens.Aggregate(h.logged(), tokens.Options{})
	got := report.Totals
	if got.InputTokens != 300 || got.OutputTokens != 20 || got.CacheReadInputTokens != 200 || got.CacheCreationInputTokens != 10 {
		t.Fatalf("totals = %+v", got)
	}
	if got.CostUSD < 0.0399 || got.CostUSD > 0.0401 {
		t.Fatalf("cost = %v, want 0.04", got.CostUSD)
	}
	if len(report.ByHarness) != 1 || report.ByHarness[0].Key != "cursor" {
		t.Fatalf("by harness = %+v", report.ByHarness)
	}
	if len(report.ByModel) != 1 || report.ByModel[0].Usage.InputTokens != 300 {
		t.Fatalf("by model = %+v", report.ByModel)
	}
	if len(report.ByUser) != 1 || report.ByUser[0].Key != "dev@example.com [cursor:42]" {
		t.Fatalf("by user = %+v, want the Cursor member", report.ByUser)
	}
}

// A team sync on one endpoint keeps each member's spend under that member.
func TestTeamUsageRollsUpPerMember(t *testing.T) {
	ana := usageAt(baseNow.Add(-2*time.Hour), 100)
	ana.UserEmail, ana.UserID = "ana@example.com", "11"
	bo := usageAt(baseNow.Add(-time.Hour), 200)
	bo.UserEmail, bo.UserID = "bo@example.com", "22"
	h := newHarness(t, ana, bo)
	h.query = Query{}
	h.sync()
	report := tokens.Aggregate(h.logged(), tokens.Options{})
	got := map[string]int64{}
	for _, g := range report.ByUser {
		got[g.Key] = g.Usage.InputTokens
	}
	if len(got) != 2 || got["ana@example.com [cursor:11]"] != 100 || got["bo@example.com [cursor:22]"] != 200 {
		t.Fatalf("by user = %#v", got)
	}
}

func TestMemberUser(t *testing.T) {
	if u, ok := MemberUser(UsageEvent{UserID: "42", UserEmail: "dev@example.com"}); !ok || u.UID != "cursor:42" || u.Name != "dev@example.com" {
		t.Fatalf("member = %+v %v", u, ok)
	}
	if u, ok := MemberUser(UsageEvent{UserEmail: "dev@example.com"}); !ok || u.UID != "cursor:dev@example.com" {
		t.Fatalf("email-only member = %+v %v", u, ok)
	}
	if _, ok := MemberUser(UsageEvent{}); ok {
		t.Fatal("an event naming no member keeps the local user")
	}
	if out := MapEvent(UsageEvent{Timestamp: baseNow, Model: "m"}); out.User.UID == "" && out.User.Name == "" && os.Getenv("USER") != "" {
		t.Fatal("an event naming no member must keep the local user from NewEvent")
	}
}
