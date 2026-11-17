package threatrules

import (
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
// derived field holds) changes what rules say about events, and a rule's own fixtures only show
// the part its author thought of. A Snapshot is what a pack says about a set of events, expression
// by expression; two of them, taken before and after a change, show everything the change did.

// Snapshot records, for each event of a set, the expressions of a rule pack that are true for it.
type Snapshot struct {
	// Matchers names the expressions the snapshot asked, sorted: a single-event rule by its ID,
	// a correlation step as "<rule ID>#<step ID>".
	Matchers []string `json:"matchers"`
	// Events are the events asked about, sorted by ID.
	Events []EventVerdicts `json:"events"`
	// Errors counts the evaluations that failed (one that exceeded the cost limit, for one);
	// each counts as no match. FirstError is the first of them.
	Errors     int    `json:"errors,omitempty"`
	FirstError string `json:"first_error,omitempty"`
}

// EventVerdicts is one event of a snapshot and the matchers true for it.
type EventVerdicts struct {
	// ID identifies the event by its content: two snapshots are compared on the IDs they share.
	ID string `json:"id"`
	// Label says what the event is, for a person: its action and its command, path or tool.
	Label   string   `json:"label"`
	Matches []string `json:"matches,omitempty"`
}

// FixtureEvents returns the distinct events of the rules' fixtures, in the order the rules
// list them: the events a pack's authors chose to show what its rules do.
func FixtureEvents(rules []*Rule) []asymptoteobserve.Event {
	seen := map[string]bool{}
	var events []asymptoteobserve.Event
	for _, r := range rules {
		for _, fixture := range r.Tests {
			for _, event := range fixture.EventList() {
				if id := eventID(event); !seen[id] {
					seen[id] = true
					events = append(events, event)
				}
			}
		}
	}
	return events
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

// TakeSnapshot evaluates every expression of the rules on every one of the events, each on its
// own: a single-event rule's match, and each step of a correlation rule, which is what the rule
// can be made of. Events with the same content are asked about once. A rule whose ID another
// rule before it has (the baseline's copy of a corpus rule) is left out.
func TakeSnapshot(rules []*Rule, events []asymptoteobserve.Event) (*Snapshot, error) {
	type expression struct {
		name  string
		match matcher
	}
	var named []expression
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.ID] {
			continue
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
		for i, step := range r.Correlation.Steps {
			named = append(named, expression{r.ID + "#" + step.ID, c.steps[i]})
		}
	}
	sort.SliceStable(named, func(i, j int) bool { return named[i].name < named[j].name })

	s := &Snapshot{}
	match := make([]matcher, len(named))
	for i, x := range named {
		s.Matchers = append(s.Matchers, x.name)
		match[i] = x.match
	}
	all := newExpressions(match)
	asked := map[string]bool{}
	for _, event := range events {
		id := eventID(event)
		if asked[id] {
			continue
		}
		asked[id] = true
		verdicts := EventVerdicts{ID: id, Label: eventLabel(event)}
		all.evaluate(&eventInput{event: &event}, nil, func(i int, held bool, err error) bool {
			// A failure is counted and the snapshot goes on, as a scan does.
			if err != nil {
				if s.Errors == 0 {
					s.FirstError = fmt.Sprintf("%s on %s: %v", named[i].name, verdicts.Label, err)
				}
				s.Errors++
				return true
			}
			if held {
				verdicts.Matches = append(verdicts.Matches, named[i].name)
			}
			return true
		})
		s.Events = append(s.Events, verdicts)
	}
	sort.Slice(s.Events, func(i, j int) bool { return s.Events[i].ID < s.Events[j].ID })
	return s, nil
}

// SnapshotDiff is what changed between two snapshots, over the events both asked about.
type SnapshotDiff struct {
	// Compared is the number of events both snapshots hold; OnlyBefore and OnlyAfter count the
	// events one of them holds alone, which are not compared.
	Compared, OnlyBefore, OnlyAfter int
	// Changes lists the matchers whose verdict changed on some event, or that only one
	// snapshot has, sorted by name.
	Changes []MatcherChange
}

// MatcherChange is how one rule or correlation step changed.
type MatcherChange struct {
	Matcher string
	// Added is set for a matcher only the later snapshot has, and Removed for one only the
	// earlier has. Gained and Lost then list every event it matches.
	Added, Removed bool
	// Gained are the labels of the events it matches now and did not, and Lost of those it
	// matched and does not, sorted, one per event: two events may have the same label (a long
	// command line is cut, and events differ in more than their label), and each counts.
	Gained, Lost []string
}

// Changed reports whether anything differs.
func (d SnapshotDiff) Changed() bool { return len(d.Changes) > 0 }

// Diff compares the snapshot with a later one.
func (before *Snapshot) Diff(after *Snapshot) SnapshotDiff {
	var diff SnapshotDiff
	later := make(map[string]*EventVerdicts, len(after.Events))
	for i := range after.Events {
		later[after.Events[i].ID] = &after.Events[i]
	}
	changes := map[string]*MatcherChange{}
	change := func(matcher string) *MatcherChange {
		c := changes[matcher]
		if c == nil {
			c = &MatcherChange{Matcher: matcher}
			changes[matcher] = c
		}
		return c
	}
	for _, m := range before.Matchers {
		if !slices.Contains(after.Matchers, m) {
			change(m).Removed = true
		}
	}
	for _, m := range after.Matchers {
		if !slices.Contains(before.Matchers, m) {
			change(m).Added = true
		}
	}
	for i := range before.Events {
		was := &before.Events[i]
		is, ok := later[was.ID]
		if !ok {
			diff.OnlyBefore++
			continue
		}
		diff.Compared++
		for _, m := range was.Matches {
			if !slices.Contains(is.Matches, m) {
				c := change(m)
				c.Lost = append(c.Lost, was.Label)
			}
		}
		for _, m := range is.Matches {
			if !slices.Contains(was.Matches, m) {
				c := change(m)
				c.Gained = append(c.Gained, is.Label)
			}
		}
	}
	diff.OnlyAfter = len(after.Events) - diff.Compared
	for _, c := range changes {
		slices.Sort(c.Gained)
		slices.Sort(c.Lost)
		diff.Changes = append(diff.Changes, *c)
	}
	sort.Slice(diff.Changes, func(i, j int) bool { return diff.Changes[i].Matcher < diff.Changes[j].Matcher })
	return diff
}
