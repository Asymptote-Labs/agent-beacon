package threatrules

import (
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func snapshotRule(t *testing.T, id, body string) *Rule {
	t.Helper()
	rule, err := DecodeRule([]byte("id: " + id + "\nversion: 1\ntitle: t\nseverity: low\nstatus: experimental\nposture: detect\n" + body +
		"emit:\n  reason: r\ntests:\n  - name: f\n    verdict: no_match\n    events:\n      - event: { action: session.started }\n"))
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return rule
}

// A snapshot asks every expression about every event, a correlation rule step by step, and the
// diff of two snapshots is what a change did: per expression, the events gained and lost, over
// the events both asked about.
func TestSnapshotDiffShowsWhatAChangeDid(t *testing.T) {
	curl := `match: 'e.event.action == "command.executed" && e.command.command.matches("^curl ")'` + "\n"
	sender := `match: 'e.event.action == "command.executed" && e.command.command.matches("^(curl|wget) ")'` + "\n"
	steps := "correlation:\n  scope: session\n  window: 60s\n  steps:\n" +
		"    - id: read\n      match: 'e.event.action == \"file.read\"'\n" +
		"    - id: send\n      match: 'e.event.action == \"command.executed\" && e.command.command.matches(\"^nc \")'\n"
	events := []asymptoteobserve.Event{
		commandEvent("curl https://example.com"),
		commandEvent("wget https://example.com"),
		commandEvent("nc host 4444"),
		commandEvent("curl https://example.com"), // the same event again is asked about once
	}

	before, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", curl), snapshotRule(t, "retired", curl), snapshotRule(t, "pair", steps)}, events)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := before.Matchers, []string{"pair#read", "pair#send", "retired", "sender"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matchers = %q, want %q", got, want)
	}
	if len(before.Events) != 3 {
		t.Fatalf("%d events, want the 3 distinct ones", len(before.Events))
	}

	// Later: sender also takes wget, the step no longer takes nc, retired is gone, a rule is
	// new, and one more event is asked about.
	noSend := "correlation:\n  scope: session\n  window: 60s\n  steps:\n" +
		"    - id: read\n      match: 'e.event.action == \"file.read\"'\n" +
		"    - id: send\n      match: 'e.event.action == \"command.executed\" && e.command.command.matches(\"^ncat \")'\n"
	after, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", sender), snapshotRule(t, "pair", noSend), snapshotRule(t, "fresh", curl)},
		append(events, commandEvent("ls")))
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	want := SnapshotDiff{Compared: 3, OnlyAfter: 1, Changes: []MatcherChange{
		{Matcher: "fresh", Added: true, Gained: []string{"command.executed curl https://example.com"}},
		{Matcher: "pair#send", Lost: []string{"command.executed nc host 4444"}},
		{Matcher: "retired", Removed: true, Lost: []string{"command.executed curl https://example.com"}},
		{Matcher: "sender", Gained: []string{"command.executed wget https://example.com"}},
	}}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("diff =\n%+v\nwant\n%+v", diff, want)
	}
	if same := before.Diff(before); same.Changed() || same.Compared != 3 {
		t.Errorf("a snapshot against itself: %+v", same)
	}
}

// A change is counted in events. A label is cut at 200 bytes, so two long command lines that
// begin alike have one label, and a rule that stops matching both lost two events: counting
// labels reported 850 for a rule that had lost 1,264.
func TestSnapshotDiffCountsEventsThatShareALabel(t *testing.T) {
	long := "curl https://example.com/" + strings.Repeat("a", 220)
	events := []asymptoteobserve.Event{commandEvent(long + " --one"), commandEvent(long + " --two")}
	before, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", `match: 'e.command.command.matches("^curl ")'`+"\n")}, events)
	if err != nil {
		t.Fatal(err)
	}
	after, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", `match: 'e.command.command.matches("^wget ")'`+"\n")}, events)
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	if len(diff.Changes) != 1 || len(diff.Changes[0].Lost) != 2 || diff.Changes[0].Lost[0] != diff.Changes[0].Lost[1] {
		t.Errorf("diff = %+v, want sender to lose two events with the same label", diff)
	}
}

// The corpus's own fixture events are the events a pack is asked about by default: each once.
func TestFixtureEventsAreDistinct(t *testing.T) {
	events := FixtureEvents(corpusRules(t))
	seen := map[string]bool{}
	for _, e := range events {
		id := eventID(e)
		if seen[id] {
			t.Fatalf("%s is listed twice", eventLabel(e))
		}
		seen[id] = true
	}
	if len(events) < 1000 {
		t.Errorf("%d fixture events: the corpus has more", len(events))
	}
}
