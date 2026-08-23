package threatrules

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// actions renders a sorted stream compactly so a failure shows the order it produced.
func actions(events []asymptoteobserve.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Event.Action)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSortEventsPutsAppendOrderIntoEmissionOrder(t *testing.T) {
	// The append order a real runtime log lands in: the collector exporter flushes on its
	// export interval, so its 10:00:06 event is written before the hook's synchronous
	// 10:00:01 event even though the hook's event happened first.
	events := []asymptoteobserve.Event{
		corrEvent("2026-08-21T10:00:06.000000000Z", "session.activity", "s1", nil),
		corrEvent("2026-08-21T10:00:01.000000000Z", "file.read", "s1", withEnv),
		corrEvent("2026-08-21T10:00:03.000000000Z", "command.executed", "s1", withCurl),
	}
	SortEvents(events)
	want := []string{"file.read", "command.executed", "session.activity"}
	if got := actions(events); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSortEventsMakesOrderedCorrelationFireOnAppendOrder(t *testing.T) {
	// The failure this workstream is about: read-then-egress happened in order, but the
	// egress event reached the log first, so the ordered-window rule saw the reverse and
	// silently missed.
	c := readThenEgressRule(t)
	appended := []asymptoteobserve.Event{
		corrEvent("2026-08-21T10:00:30.000000000Z", "command.executed", "s1", withCurl),
		corrEvent("2026-08-21T10:00:00.000000000Z", "file.read", "s1", withEnv),
	}

	verdict, err := c.Evaluate(appended)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if verdict != VerdictNoMatch {
		t.Fatalf("append order should miss (that is the bug), got %s", verdict)
	}

	SortEvents(appended)
	verdict, err = c.Evaluate(appended)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if verdict != VerdictMatch {
		t.Fatalf("sorted stream want match, got %s", verdict)
	}
}

func TestSortEventsBreaksTimestampTiesOnSequence(t *testing.T) {
	// One metric export flushes datapoints that all carry that export's collection
	// instant, so they tie however precise the timestamp is. Sequence is the only thing
	// left that knows which came first.
	tie := "2026-08-21T10:00:00.000000000Z"
	events := []asymptoteobserve.Event{
		corrEvent(tie, "third", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 30 }),
		corrEvent(tie, "first", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 10 }),
		corrEvent(tie, "second", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 20 }),
	}
	SortEvents(events)
	want := []string{"first", "second", "third"}
	if got := actions(events); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSortEventsKeepsTimestampAheadOfSequence(t *testing.T) {
	// Sequence orders one writer's stream, not the log: the hook process that emitted
	// sequence 1 may have started long after the exporter reached sequence 900. The
	// timestamp is the only cross-writer key, so it has to win.
	events := []asymptoteobserve.Event{
		corrEvent("2026-08-21T10:00:05.000000000Z", "hook", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 1 }),
		corrEvent("2026-08-21T10:00:01.000000000Z", "exporter", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 900 }),
	}
	SortEvents(events)
	if got := actions(events); !equalStrings(got, []string{"exporter", "hook"}) {
		t.Fatalf("order = %v, want exporter before hook", got)
	}
}

func TestSortEventsIgnoresUnsequencedTie(t *testing.T) {
	// A zero sequence means "unsequenced", not "first". Events written before sequence
	// stamping must keep the order they arrived in rather than being dragged ahead of
	// everything that does carry a counter.
	tie := "2026-08-21T10:00:00.000000000Z"
	events := []asymptoteobserve.Event{
		corrEvent(tie, "legacy", "s1", nil),
		corrEvent(tie, "sequenced", "s1", func(e *asymptoteobserve.Event) { e.Sequence = 7 }),
	}
	SortEvents(events)
	if got := actions(events); !equalStrings(got, []string{"legacy", "sequenced"}) {
		t.Fatalf("order = %v, want input order preserved", got)
	}
}

func TestSortEventsKeepsUnstampedEventsBesideTheirNeighbours(t *testing.T) {
	// An event with no parseable timestamp inherits the last one seen, so it stays where
	// it arrived instead of collapsing to the epoch and jumping to the front.
	events := []asymptoteobserve.Event{
		corrEvent("2026-08-21T10:00:05.000000000Z", "late", "s1", nil),
		corrEvent("2026-08-21T10:00:01.000000000Z", "early", "s1", nil),
		corrEvent("", "early-companion", "s1", nil),
		corrEvent("not-a-timestamp", "early-companion-2", "s1", nil),
	}
	SortEvents(events)
	want := []string{"early", "early-companion", "early-companion-2", "late"}
	if got := actions(events); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSortEventsLeavesAnUnstampedStreamAlone(t *testing.T) {
	// Conformance fixtures carry no timestamps at all; their given order is the sequence
	// under test and must survive.
	events := []asymptoteobserve.Event{
		corrEvent("", "one", "s1", nil),
		corrEvent("", "two", "s1", nil),
		corrEvent("", "three", "s1", nil),
	}
	SortEvents(events)
	if got := actions(events); !equalStrings(got, []string{"one", "two", "three"}) {
		t.Fatalf("order = %v, want input order preserved", got)
	}
}

func TestSortEventsIsATotalOrder(t *testing.T) {
	// Same log in, same order out -- including for events that tie on every key, which
	// fall back to input position rather than to whatever the sort implementation does.
	tie := "2026-08-21T10:00:00.000000000Z"
	build := func() []asymptoteobserve.Event {
		return []asymptoteobserve.Event{
			corrEvent(tie, "a", "s1", nil),
			corrEvent(tie, "b", "s2", nil),
			corrEvent(tie, "c", "s1", nil),
			corrEvent("2026-08-21T09:59:59.999999999Z", "d", "s1", nil),
		}
	}
	first := build()
	SortEvents(first)
	for i := 0; i < 5; i++ {
		again := build()
		SortEvents(again)
		if !equalStrings(actions(first), actions(again)) {
			t.Fatalf("run %d = %v, want %v", i, actions(again), actions(first))
		}
	}
	if got := actions(first); !equalStrings(got, []string{"d", "a", "b", "c"}) {
		t.Fatalf("order = %v, want [d a b c]", got)
	}
}

func TestSortEventsHandlesShortStreams(t *testing.T) {
	SortEvents(nil)
	one := []asymptoteobserve.Event{corrEvent("2026-08-21T10:00:00.000000000Z", "only", "s1", nil)}
	SortEvents(one)
	if got := actions(one); !equalStrings(got, []string{"only"}) {
		t.Fatalf("order = %v, want [only]", got)
	}
}

func TestSortEventsOrdersAcrossSessions(t *testing.T) {
	// Correlation groups by session after the sort, so interleaved sessions have to come
	// out of the sort correctly ordered within each one.
	events := []asymptoteobserve.Event{
		corrEvent("2026-08-21T10:00:04.000000000Z", "s2-second", "s2", nil),
		corrEvent("2026-08-21T10:00:03.000000000Z", "s1-second", "s1", nil),
		corrEvent("2026-08-21T10:00:02.000000000Z", "s2-first", "s2", nil),
		corrEvent("2026-08-21T10:00:01.000000000Z", "s1-first", "s1", nil),
	}
	SortEvents(events)
	groups, order := groupBySession(events)
	if !equalStrings(order, []string{"s1", "s2"}) {
		t.Fatalf("session order = %v, want [s1 s2]", order)
	}
	if got := actions(groups["s1"]); !equalStrings(got, []string{"s1-first", "s1-second"}) {
		t.Fatalf("s1 = %v", got)
	}
	if got := actions(groups["s2"]); !equalStrings(got, []string{"s2-first", "s2-second"}) {
		t.Fatalf("s2 = %v", got)
	}
}
