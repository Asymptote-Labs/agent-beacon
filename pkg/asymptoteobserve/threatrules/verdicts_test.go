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

// one is a single sequence of the events.
func one(events ...asymptoteobserve.Event) []EventSequence {
	return []EventSequence{{Label: "events", Events: events}}
}

func refTo(e asymptoteobserve.Event) EventRef { return EventRef{ID: eventID(e), Label: eventLabel(e)} }

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

	before, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", curl), snapshotRule(t, "retired", curl), snapshotRule(t, "pair", steps)}, one(events...))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := before.Matchers, []string{"pair#read", "pair#send", "retired", "sender"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matchers = %q, want %q", got, want)
	}
	if got, want := before.Correlations, []string{"pair"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("correlations = %q, want %q", got, want)
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
		one(append(events, commandEvent("ls"))...))
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	// The later sequence holds one more event, so it is another sequence: counted, not compared.
	want := SnapshotDiff{Compared: 3, OnlyAfter: 1, SequencesOnlyBefore: 1, SequencesOnlyAfter: 1, Changes: []MatcherChange{
		{Matcher: "fresh", Added: true, Gained: []EventRef{refTo(events[0])}},
		{Matcher: "pair#send", Lost: []EventRef{refTo(events[2])}},
		{Matcher: "retired", Removed: true, Lost: []EventRef{refTo(events[0])}},
		{Matcher: "sender", Gained: []EventRef{refTo(events[1])}},
	}}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("diff =\n%+v\nwant\n%+v", diff, want)
	}
	if same := before.Diff(before); same.Changed() || same.Compared != 3 || same.Sequences != 1 {
		t.Errorf("a snapshot against itself: %+v", same)
	}
}

// A change is counted in events. A label is cut at 200 bytes, so two long command lines that
// begin alike have one label, and a rule that stops matching both lost two events: counting
// labels reported 850 for a rule that had lost 1,264.
func TestSnapshotDiffCountsEventsThatShareALabel(t *testing.T) {
	long := "curl https://example.com/" + strings.Repeat("a", 220)
	events := one(commandEvent(long+" --one"), commandEvent(long+" --two"))
	before, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", `match: 'e.command.command.matches("^curl ")'`+"\n")}, events)
	if err != nil {
		t.Fatal(err)
	}
	after, err := TakeSnapshot([]*Rule{snapshotRule(t, "sender", `match: 'e.command.command.matches("^wget ")'`+"\n")}, events)
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	if len(diff.Changes) != 1 || len(diff.Changes[0].Lost) != 2 || diff.Changes[0].Lost[0].Label != diff.Changes[0].Lost[1].Label ||
		diff.Changes[0].Lost[0].ID == diff.Changes[0].Lost[1].ID {
		t.Errorf("diff = %+v, want sender to lose two events with the same label", diff)
	}
}

// A correlation rule is matched over each sequence as a scan matches it, so a change to what a
// correlation finds shows although no step changed: here the window, which is the scan's.
func TestSnapshotDiffShowsWhatACorrelationFinds(t *testing.T) {
	pair := func(window string) *Rule {
		return snapshotRule(t, "pair", "correlation:\n  scope: session\n  window: "+window+"\n  steps:\n"+
			"    - id: read\n      match: 'e.event.action == \"file.read\"'\n"+
			"    - id: send\n      match: 'e.event.action == \"command.executed\"'\n")
	}
	read := corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv)
	send := corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl)
	sequences := []EventSequence{{Label: "read then send", Events: []asymptoteobserve.Event{read, send}}}
	before, err := TakeSnapshot([]*Rule{pair("60s")}, sequences)
	if err != nil {
		t.Fatal(err)
	}
	after, err := TakeSnapshot([]*Rule{pair("10s")}, sequences)
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	lost := EventRef{
		ID:    sequenceID([]string{eventID(read), eventID(send)}) + " " + eventID(read) + "+" + eventID(send),
		Label: "read then send: " + eventLabel(read) + " → " + eventLabel(send),
	}
	want := SnapshotDiff{Compared: 2, Sequences: 1, Changes: []MatcherChange{{Matcher: "pair", Lost: []EventRef{lost}}}}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("diff =\n%+v\nwant\n%+v", diff, want)
	}
}

// A log that gained an event since the snapshot is a different sequence: what the correlation
// rules find over it is not compared, and the diff counts it on each side rather than leaving
// it out without a word.
func TestSnapshotDiffCountsASequenceThatChanged(t *testing.T) {
	pair := snapshotRule(t, "pair", "correlation:\n  scope: session\n  window: 60s\n  steps:\n"+
		"    - id: read\n      match: 'e.event.action == \"file.read\"'\n"+
		"    - id: send\n      match: 'e.event.action == \"command.executed\"'\n")
	read := corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv)
	send := corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl)
	later := corrEvent("2026-06-13T10:00:40Z", "command.executed", "s1", nil)
	before, err := TakeSnapshot([]*Rule{pair}, []EventSequence{{Label: "log", Events: []asymptoteobserve.Event{read, send}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := TakeSnapshot([]*Rule{pair}, []EventSequence{{Label: "log", Events: []asymptoteobserve.Event{read, send, later}}})
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	if diff.Sequences != 0 || diff.SequencesOnlyBefore != 1 || diff.SequencesOnlyAfter != 1 {
		t.Errorf("diff = %+v; want the changed log counted once on each side and not compared", diff)
	}
}

// An expression that now fails on an event it answered before is a change: its verdict there
// is unknown. A snapshot written before failures were recorded is compared on them only when
// none failed in it.
func TestSnapshotDiffCountsANewFailure(t *testing.T) {
	lowerCostLimit(t)
	event := commandEvent("ls")
	cheap := snapshotRule(t, "slow", `match: 'e.event.action == "x"'`+"\n")
	runaway := snapshotRule(t, "slow", "match: '"+nestedAll(7)+"'\n")
	before, err := TakeSnapshot([]*Rule{cheap}, one(event))
	if err != nil {
		t.Fatal(err)
	}
	after, err := TakeSnapshot([]*Rule{runaway}, one(event))
	if err != nil {
		t.Fatal(err)
	}
	if after.Errors != 1 || !strings.Contains(after.FirstError, "cost limit") {
		t.Fatalf("snapshot errors %d, first %q; want the runaway's", after.Errors, after.FirstError)
	}
	diff := before.Diff(after)
	want := SnapshotDiff{Compared: 1, Sequences: 0, Changes: []MatcherChange{{Matcher: "slow", Failing: []EventRef{refTo(event)}}}}
	if !diff.Changed() || !reflect.DeepEqual(diff, want) {
		t.Errorf("diff =\n%+v\nwant\n%+v", diff, want)
	}
	if back := after.Diff(before); !reflect.DeepEqual(back.Changes, []MatcherChange{{Matcher: "slow", Recovered: []EventRef{refTo(event)}}}) {
		t.Errorf("the other way: %+v", back.Changes)
	}

	// An older snapshot with errors does not say which failed, and is not compared on them.
	older := *after
	older.Format, older.Events = 0, []EventVerdicts{{ID: after.Events[0].ID, Label: after.Events[0].Label}}
	if diff := before.Diff(&older); diff.Changed() {
		t.Errorf("against a snapshot that does not record its failures: %+v", diff.Changes)
	}
}

// The corpus's fixtures, and its predicates', are what a pack is asked about by default.
func TestFixtureSequencesHoldThePredicatesFixtures(t *testing.T) {
	rules := corpusRules(t)
	sequences := FixtureSequences(rules)
	labels := map[string]bool{}
	events := map[string]bool{}
	for _, s := range sequences {
		labels[s.Label] = true
		for _, e := range s.Events {
			events[eventID(e)] = true
		}
	}
	if !anyHasPrefix(labels, "credential_file_read: ") {
		t.Errorf("no fixture of credential_file_read among %d sequences", len(sequences))
	}
	if len(events) < 1000 {
		t.Errorf("%d distinct fixture events: the corpus has more", len(events))
	}
}

func anyHasPrefix(set map[string]bool, prefix string) bool {
	for s := range set {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
