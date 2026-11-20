package threatrules

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// A change to a rule, to a predicate or a catalog every rule shares, or to the engine (what a
// derived field holds, how a scan matches a correlation) changes what rules say about events, and
// a rule's own fixtures only show the part its author thought of. A Snapshot is what a pack says
// about a set of events, through the code a scan runs; two of them, taken before and after a
// change, show everything the change did.

// Snapshot records what a rule pack says about a set of events: for each event, the expressions
// true for it and those that could not be evaluated on it; and for each ordered list of events (a
// fixture, a log), what the correlation rules find in it as a scan finds it.
type Snapshot struct {
	// Format is snapshotFormat for a snapshot that records failed evaluations and sequences. One
	// written before them has neither, and is compared on what it has.
	Format int `json:"format,omitempty"`
	// Matchers names the expressions the snapshot asked of each event, sorted: a single-event
	// rule by its ID, a correlation step as "<rule ID>#<step ID>".
	Matchers []string `json:"matchers"`
	// Correlations names the correlation rules the snapshot ran on each sequence, sorted.
	Correlations []string `json:"correlations,omitempty"`
	// Events are the events asked about, sorted by ID.
	Events []EventVerdicts `json:"events"`
	// Sequences are the sequences the correlation rules ran on, sorted by ID.
	Sequences []SequenceVerdicts `json:"sequences,omitempty"`
	// Errors counts the evaluations that failed (one that exceeded the cost limit, for one): an
	// expression on an event, or a correlation rule's step in a sequence. FirstError is the
	// first of them.
	Errors     int    `json:"errors,omitempty"`
	FirstError string `json:"first_error,omitempty"`
}

// snapshotFormat is the Format of the snapshots TakeSnapshot takes.
const snapshotFormat = 2

// EventVerdicts is one event of a snapshot: the matchers true for it, and those that failed on it.
type EventVerdicts struct {
	// ID identifies the event by its content: two snapshots are compared on the IDs they share.
	ID string `json:"id"`
	// Label says what the event is, for a person: its action and its command, path or tool.
	Label   string   `json:"label"`
	Matches []string `json:"matches,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// SequenceVerdicts is one sequence of a snapshot and what the correlation rules found in it.
type SequenceVerdicts struct {
	// ID identifies the sequence by its events, in order.
	ID    string `json:"id"`
	Label string `json:"label"`
	// Found is each finding, sorted: the rule's ID, a space, and the IDs of its evidence
	// events joined by "+".
	Found []string `json:"found,omitempty"`
	// Failed names the rules a step of which could not be evaluated on an event they asked it
	// of in the sequence.
	Failed []string `json:"failed,omitempty"`
}

// EventSequence is an ordered list of events a pack is asked about: a fixture's, or a log's.
// Each event is asked about on its own, and the correlation rules are matched over the whole.
type EventSequence struct {
	Label  string
	Events []asymptoteobserve.Event
}

// FixtureSequences returns the fixtures of the rules, and of the predicates of the definitions
// they were loaded with, in the order the rules list them: the events a pack's authors chose to
// show what its rules and predicates do.
func FixtureSequences(rules []*Rule) []EventSequence {
	var sequences []EventSequence
	var libs []*Library
	for _, r := range rules {
		for _, fixture := range r.Tests {
			sequences = append(sequences, EventSequence{Label: r.ID + ": " + fixture.Name, Events: fixture.EventList()})
		}
		if r.lib != nil && !slices.Contains(libs, r.lib) {
			libs = append(libs, r.lib)
		}
	}
	for _, lib := range libs {
		for _, t := range lib.tests {
			if len(t.test.Events) == 0 {
				continue // a catalog's examples: paths and commands, not events
			}
			events := make([]asymptoteobserve.Event, len(t.test.Events))
			for i := range t.test.Events {
				events[i] = t.test.Events[i].Event
			}
			sequences = append(sequences, EventSequence{Label: t.predicate + ": " + t.test.Name, Events: events})
		}
	}
	return sequences
}

// eventID identifies an event by its content.
func eventID(event asymptoteobserve.Event) string {
	raw, err := json.Marshal(event)
	if err != nil {
		raw = fmt.Appendf(nil, "%+v", event)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// sequenceID identifies a sequence by the IDs of its events, in order.
func sequenceID(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, "+")))
	return hex.EncodeToString(sum[:8])
}

// eventLabel says what an event is in one line.
func eventLabel(event asymptoteobserve.Event) string {
	label := event.Event.Action
	switch {
	case event.Command != nil && event.Command.Command != "":
		label += " " + event.Command.Command
	case event.File != nil && event.File.Path != "":
		label += " " + event.File.Path
	case event.MCP != nil && (event.MCP.Server != "" || event.MCP.Tool != ""):
		label += " " + event.MCP.Server + "/" + event.MCP.Tool
	}
	label = strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(label)
	if len(label) > 200 {
		label = label[:200] + "..."
	}
	return label
}

// TakeSnapshot asks the rules about the sequences as a scan does. Every expression is evaluated
// on every event of every sequence on its own: a single-event rule's match, and each step of a
// correlation rule, which is what the rule can be made of. Then the correlation rules are
// matched over each sequence (Scan), which is what the steps make. Events and sequences with
// the same content are asked about once. An evaluation that fails is recorded where it failed
// and the snapshot goes on, as a scan does. The rules' IDs must be distinct.
func TakeSnapshot(rules []*Rule, sequences []EventSequence) (*Snapshot, error) {
	type expression struct {
		name  string
		match matcher
	}
	var named []expression
	var correlations []*CompiledRule
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.ID] {
			return nil, fmt.Errorf("rule %q is listed twice", r.ID)
		}
		seen[r.ID] = true
		c, err := Compile(r)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.ID, err)
		}
		if c.steps == nil {
			named = append(named, expression{r.ID, c.match})
			continue
		}
		correlations = append(correlations, c)
		for i, step := range r.Correlation.Steps {
			named = append(named, expression{r.ID + "#" + step.ID, c.steps[i]})
		}
	}
	sort.SliceStable(named, func(i, j int) bool { return named[i].name < named[j].name })
	slices.SortFunc(correlations, func(a, b *CompiledRule) int { return cmp.Compare(a.rule.ID, b.rule.ID) })

	s := &Snapshot{Format: snapshotFormat}
	failed := func(what, where string, err error) {
		if s.Errors == 0 {
			s.FirstError = fmt.Sprintf("%s on %s: %v", what, where, err)
		}
		s.Errors++
	}
	match := make([]matcher, len(named))
	for i, x := range named {
		s.Matchers = append(s.Matchers, x.name)
		match[i] = x.match
	}
	for _, c := range correlations {
		s.Correlations = append(s.Correlations, c.rule.ID)
	}
	all := newExpressions(match)
	asked, ran := map[string]bool{}, map[string]bool{} // events, and sequences
	for _, sequence := range sequences {
		ids := make([]string, len(sequence.Events))
		for k, event := range sequence.Events {
			ids[k] = eventID(event)
			if asked[ids[k]] {
				continue
			}
			asked[ids[k]] = true
			verdicts := EventVerdicts{ID: ids[k], Label: eventLabel(event)}
			all.evaluate(&eventInput{event: &event}, nil, func(i int, held bool, err error) bool {
				switch {
				case err != nil:
					verdicts.Failed = append(verdicts.Failed, named[i].name)
					failed(named[i].name, verdicts.Label, err)
				case held:
					verdicts.Matches = append(verdicts.Matches, named[i].name)
				}
				return true
			})
			s.Events = append(s.Events, verdicts)
		}
		id := sequenceID(ids)
		if len(correlations) == 0 || len(sequence.Events) == 0 || ran[id] {
			continue
		}
		ran[id] = true
		result := Scan(correlations, sequence.Events)
		verdicts := SequenceVerdicts{ID: id, Label: sequence.Label}
		for _, f := range result.Findings {
			evidence := make([]string, len(f.Events))
			for k := range f.Events {
				evidence[k] = eventID(f.Events[k])
			}
			verdicts.Found = append(verdicts.Found, f.RuleID+" "+strings.Join(evidence, "+"))
		}
		for _, f := range result.Failures {
			if !slices.Contains(verdicts.Failed, f.RuleID) {
				verdicts.Failed = append(verdicts.Failed, f.RuleID)
			}
			failed(f.RuleID, sequence.Label, f.Err)
		}
		slices.Sort(verdicts.Found)
		slices.Sort(verdicts.Failed)
		s.Sequences = append(s.Sequences, verdicts)
	}
	sort.Slice(s.Events, func(i, j int) bool { return s.Events[i].ID < s.Events[j].ID })
	sort.Slice(s.Sequences, func(i, j int) bool { return s.Sequences[i].ID < s.Sequences[j].ID })
	return s, nil
}

// recordsFailures reports whether the snapshot says which evaluations failed: it is of the
// current format, or none failed.
func (s *Snapshot) recordsFailures() bool { return s.Format >= snapshotFormat || s.Errors == 0 }

// SnapshotDiff is what changed between two snapshots, over the events and sequences both asked
// about.
type SnapshotDiff struct {
	// Compared is the number of events both snapshots hold; OnlyBefore and OnlyAfter count the
	// events one of them holds alone, which are not compared.
	Compared, OnlyBefore, OnlyAfter int
	// Sequences is the number of sequences both snapshots ran the correlation rules on. A
	// snapshot written before sequences were recorded has none. SequencesOnlyBefore and
	// SequencesOnlyAfter count the sequences one of them holds alone, which are not compared:
	// a log that gained or lost an event since the snapshot is a different sequence.
	Sequences, SequencesOnlyBefore, SequencesOnlyAfter int
	// Changes lists the matchers whose verdict changed on some event or sequence, or that only
	// one snapshot has, sorted by name.
	Changes []MatcherChange
}

// MatcherChange is how one rule or correlation step changed.
type MatcherChange struct {
	Matcher string
	// Added is set for a matcher only the later snapshot has, and Removed for one only the
	// earlier has. Gained and Lost then list every event it matches.
	Added, Removed bool
	// Gained are the events it matches now and did not, and Lost those it matched and does
	// not; for a correlation rule, its findings in a sequence, each named by the sequence and
	// its evidence. Each is listed once, sorted by label: two may have the same label (a long
	// command line is cut, and events differ in more than their label).
	Gained, Lost []EventRef
	// Failing are the events (for a correlation rule, the sequences) it could be evaluated on
	// and now cannot, and Recovered those it can be again: a verdict that became unknown, or
	// known again, is a change as much as one that flipped.
	Failing, Recovered []EventRef
}

// EventRef is an event, or a correlation rule's finding in a sequence, as a diff names it.
type EventRef struct {
	ID    string
	Label string
}

// Changed reports whether anything differs.
func (d SnapshotDiff) Changed() bool { return len(d.Changes) > 0 }

// Diff compares the snapshot with a later one.
func (before *Snapshot) Diff(after *Snapshot) SnapshotDiff {
	var diff SnapshotDiff
	changes := map[string]*MatcherChange{}
	change := func(matcher string) *MatcherChange {
		c := changes[matcher]
		if c == nil {
			c = &MatcherChange{Matcher: matcher}
			changes[matcher] = c
		}
		return c
	}
	named := func(was, is []string) {
		for _, m := range was {
			if !slices.Contains(is, m) {
				change(m).Removed = true
			}
		}
		for _, m := range is {
			if !slices.Contains(was, m) {
				change(m).Added = true
			}
		}
	}
	// gone calls f with each entry of was that is not in is.
	gone := func(was, is []string, f func(string)) {
		for _, x := range was {
			if !slices.Contains(is, x) {
				f(x)
			}
		}
	}

	named(before.Matchers, after.Matchers)
	failures := before.recordsFailures() && after.recordsFailures()
	later := make(map[string]*EventVerdicts, len(after.Events))
	for i := range after.Events {
		later[after.Events[i].ID] = &after.Events[i]
	}
	for i := range before.Events {
		was := &before.Events[i]
		is, ok := later[was.ID]
		if !ok {
			diff.OnlyBefore++
			continue
		}
		diff.Compared++
		ref := EventRef{ID: was.ID, Label: was.Label}
		gone(was.Matches, is.Matches, func(m string) { c := change(m); c.Lost = append(c.Lost, ref) })
		gone(is.Matches, was.Matches, func(m string) { c := change(m); c.Gained = append(c.Gained, ref) })
		if failures {
			gone(is.Failed, was.Failed, func(m string) { c := change(m); c.Failing = append(c.Failing, ref) })
			gone(was.Failed, is.Failed, func(m string) { c := change(m); c.Recovered = append(c.Recovered, ref) })
		}
	}
	diff.OnlyAfter = len(after.Events) - diff.Compared

	if before.Format >= snapshotFormat && after.Format >= snapshotFormat {
		named(before.Correlations, after.Correlations)
		laterSequences := make(map[string]*SequenceVerdicts, len(after.Sequences))
		for i := range after.Sequences {
			laterSequences[after.Sequences[i].ID] = &after.Sequences[i]
		}
		for i := range before.Sequences {
			was := &before.Sequences[i]
			is, ok := laterSequences[was.ID]
			if !ok {
				diff.SequencesOnlyBefore++
				continue
			}
			diff.Sequences++
			gone(was.Found, is.Found, func(f string) { rule, ref := before.finding(was, f); c := change(rule); c.Lost = append(c.Lost, ref) })
			gone(is.Found, was.Found, func(f string) { rule, ref := after.finding(is, f); c := change(rule); c.Gained = append(c.Gained, ref) })
			ref := EventRef{ID: was.ID, Label: was.Label}
			gone(is.Failed, was.Failed, func(r string) { c := change(r); c.Failing = append(c.Failing, ref) })
			gone(was.Failed, is.Failed, func(r string) { c := change(r); c.Recovered = append(c.Recovered, ref) })
		}
		diff.SequencesOnlyAfter = len(after.Sequences) - diff.Sequences
		// A snapshot of a pack with no correlation rules ran none; its sequences are not
		// missing, so nothing is said of them.
		if len(before.Sequences) == 0 || len(after.Sequences) == 0 {
			diff.SequencesOnlyBefore, diff.SequencesOnlyAfter = 0, 0
		}
	}

	byLabel := func(a, b EventRef) int { return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.ID, b.ID)) }
	for _, c := range changes {
		for _, refs := range []*[]EventRef{&c.Gained, &c.Lost, &c.Failing, &c.Recovered} {
			slices.SortFunc(*refs, byLabel)
		}
		diff.Changes = append(diff.Changes, *c)
	}
	sort.Slice(diff.Changes, func(i, j int) bool { return diff.Changes[i].Matcher < diff.Changes[j].Matcher })
	return diff
}

// finding returns the rule of a finding of the sequence, and the finding as a diff names it:
// the sequence and the evidence events, by their IDs and their labels.
func (s *Snapshot) finding(sequence *SequenceVerdicts, found string) (string, EventRef) {
	rule, evidence, _ := strings.Cut(found, " ")
	labels := strings.Split(evidence, "+")
	for k, id := range labels {
		if i, ok := slices.BinarySearchFunc(s.Events, id, func(e EventVerdicts, id string) int { return cmp.Compare(e.ID, id) }); ok {
			labels[k] = s.Events[i].Label
		}
	}
	return rule, EventRef{ID: sequence.ID + " " + evidence, Label: sequence.Label + ": " + strings.Join(labels, " → ")}
}
