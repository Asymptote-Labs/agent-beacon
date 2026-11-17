package threatrules

import (
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// stepTable holds, for the correlation rules of one scan, whether each of their steps holds for
// each event. It is filled while the scan passes over the events, from the activation the
// single-event rules read the event through, so an event's derived fields are worked out once
// for the whole scan rather than once for every step that is asked about the event, and a step
// several rules share (the credential read of the read-then-egress rules) is evaluated once.
//
// A rule's matcher then asks the table instead of evaluating (see stepEval), in the order it
// always did, so which events match, which evidence a finding carries and which evaluations
// failed are what each rule finds on its own: an evaluation that fails is recorded with its
// error, and reported only when the matcher asks for that step on that event.
//
// Only what a matcher can ask is evaluated ahead of it: the first step of a sequence and every
// step of an order: any rule on every event, a later step of a sequence on the events that
// follow, in their session, one the step before it holds for (nextHit looks nowhere else).
// That is a matter of cost alone: what the table does not hold is evaluated when it is asked.
type stepTable struct {
	events  []asymptoteobserve.Event
	columns []stepColumn
	steps   expressions             // the columns' expressions
	of      map[*CompiledRule][]int // a rule's steps, as columns
	// known and hit are one bit per event for each column: the step was evaluated on the event,
	// and it held. failed holds the evaluations that returned an error.
	known, hit [][]uint64
	failed     map[[2]int]error // (column, event)
	// held lists, by session, the feeding columns that have held for an earlier event.
	held map[string][]bool
	// sessions are the events of each session, in the order the sessions first appear.
	sessions []sessionEvents
}

// stepColumn is one distinct step, and when a matcher can ask it.
type stepColumn struct {
	distinctStep
	// always is set when the step is asked on every event; otherwise it is asked on the events
	// after one that a column in after holds for. feeds is set on a column listed in an after.
	always bool
	after  []int
	feeds  bool
}

// sessionEvents are the events of one session and where each is in the scanned sequence.
type sessionEvents struct {
	events []asymptoteobserve.Event
	index  []int
}

// distinctStep is one distinct step of a set of correlation rules: an expression, read on one
// form of the event, and the first rule and step that write it.
type distinctStep struct {
	match matcher
	rule  *CompiledRule
	step  int
}

func stepMatchers(steps []distinctStep) []matcher {
	match := make([]matcher, len(steps))
	for i := range steps {
		match[i] = steps[i].match
	}
	return match
}

// stepKey identifies a step by what is evaluated: the expression and the definitions it was
// compiled with. The same text means another expression in a pack whose predicates say
// otherwise, and one rule set can hold rules of two packs (a store's rules beside the
// baseline's).
type stepKey struct {
	lib   *Library
	match string
}

// distinctSteps returns the distinct steps of the correlation rules among rules, in rule and
// step order, and each rule's steps as positions among them; a single-event rule has none.
//
// Which form of the event a step reads follows its rule (Compile). A step shared by a rule that
// reads derived fields and one that does not selects none itself, so it is the same on either
// form and reads the derived one.
func distinctSteps(rules []*CompiledRule) ([]distinctStep, map[*CompiledRule][]int) {
	var steps []distinctStep
	of := map[*CompiledRule][]int{}
	at := map[stepKey]int{}
	for _, r := range rules {
		if r.steps == nil {
			continue
		}
		if _, done := of[r]; done {
			continue
		}
		own := make([]int, len(r.steps))
		for s, step := range r.rule.Correlation.Steps {
			key := stepKey{lib: r.rule.lib, match: step.Match}
			k, ok := at[key]
			if !ok {
				k = len(steps)
				at[key] = k
				steps = append(steps, distinctStep{match: r.steps[s], rule: r, step: s})
			}
			steps[k].match.derived = steps[k].match.derived || r.derived
			own[s] = k
		}
		of[r] = own
	}
	return steps, of
}

// newStepTable returns the table for the correlation rules among rules over events, or nil when
// there are none.
func newStepTable(rules []*CompiledRule, events []asymptoteobserve.Event) *stepTable {
	steps, of := distinctSteps(rules)
	if len(steps) == 0 {
		return nil
	}
	t := &stepTable{events: events, of: of, steps: newExpressions(stepMatchers(steps))}
	t.columns = make([]stepColumn, len(steps))
	for c, step := range steps {
		t.columns[c].distinctStep = step
	}
	for _, r := range rules {
		own := of[r]
		for s := range own {
			c := own[s]
			switch {
			case r.anyOrder || s == 0:
				t.columns[c].always = true
			case !slices.Contains(t.columns[c].after, own[s-1]):
				t.columns[c].after = append(t.columns[c].after, own[s-1])
				t.columns[own[s-1]].feeds = true
			}
		}
	}
	words := (len(events) + 63) / 64
	t.known, t.hit = make([][]uint64, len(t.columns)), make([][]uint64, len(t.columns))
	for c := range t.columns {
		t.known[c], t.hit[c] = make([]uint64, words), make([]uint64, words)
	}
	return t
}

// sessionOf returns an event's session id, "" for an event of no session.
func sessionOf(e *asymptoteobserve.Event) string {
	if e.Session == nil {
		return ""
	}
	return e.Session.ID
}

// fill evaluates on event i, read through in, the steps a matcher can ask about it.
func (t *stepTable) fill(i int, in *eventInput) {
	sid := sessionOf(in.event)
	held := t.held[sid] // as it was before this event
	var fed []int
	t.steps.evaluate(in, func(c int) bool {
		col := &t.columns[c]
		return col.always || slices.ContainsFunc(col.after, func(before int) bool { return held != nil && held[before] })
	}, func(c int, holds bool, err error) bool {
		// Every answer is kept, a failure with its error: it is reported only if a matcher asks.
		t.record(c, i, holds, err)
		if holds && t.columns[c].feeds {
			fed = append(fed, c)
		}
		return true
	})
	if len(fed) == 0 {
		return
	}
	if held == nil {
		if t.held == nil {
			t.held = map[string][]bool{}
		}
		held = make([]bool, len(t.columns))
		t.held[sid] = held
	}
	for _, c := range fed {
		held[c] = true
	}
}

// record keeps the answer of column c on event i.
func (t *stepTable) record(c, i int, holds bool, err error) {
	word, bit := i/64, uint64(1)<<(i%64)
	t.known[c][word] |= bit
	if err != nil {
		if t.failed == nil {
			t.failed = map[[2]int]error{}
		}
		t.failed[[2]int{c, i}] = err
		return
	}
	if holds {
		t.hit[c][word] |= bit
	}
}

// holds reports whether column c holds for event i, as evaluating the step on the event does.
func (t *stepTable) holds(c, i int) (bool, error) {
	word, bit := i/64, uint64(1)<<(i%64)
	if t.known[c][word]&bit == 0 {
		t.steps.evaluate(&eventInput{event: &t.events[i]}, func(k int) bool { return k == c }, func(_ int, holds bool, err error) bool {
			t.record(c, i, holds, err)
			return true
		})
	}
	if t.hit[c][word]&bit != 0 {
		return true, nil
	}
	if t.failed != nil {
		if err, failed := t.failed[[2]int{c, i}]; failed {
			return false, err
		}
	}
	return false, nil
}

// findings returns what the rule finds in the scanned events, asking the table for every step. A
// step that could not be evaluated on an event is handed to fail, with the event's index in the
// scan, once for each event, and is taken not to hold there: the rule goes on, and finds what it
// can from the evaluations that did not fail, unless fail says to stop, which findings reports.
func (t *stepTable) findings(rule *CompiledRule, fail func(event int, err error) bool) ([]Finding, bool) {
	if t.sessions == nil {
		at := map[string]int{}
		for i := range t.events {
			sid := sessionOf(&t.events[i])
			k, seen := at[sid]
			if !seen {
				k = len(t.sessions)
				at[sid] = k
				t.sessions = append(t.sessions, sessionEvents{})
			}
			t.sessions[k].events = append(t.sessions[k].events, t.events[i])
			t.sessions[k].index = append(t.sessions[k].index, i)
		}
	}
	columns := t.of[rule]
	var findings []Finding
	var failed map[int]bool
	stopped := false
	for k := range t.sessions {
		session := &t.sessions[k]
		// The matcher returns no error: every failure becomes "does not hold" here.
		seq, _ := rule.matchSession(session.events, func(step, event int) (bool, error) {
			if stopped {
				return false, nil
			}
			i := session.index[event]
			held, err := t.holds(columns[step], i)
			if err != nil {
				if !failed[i] {
					if failed == nil {
						failed = map[int]bool{}
					}
					failed[i] = true
					stopped = !fail(i, err)
				}
				return false, nil
			}
			return held, nil
		})
		if stopped {
			return findings, false
		}
		if seq != nil {
			findings = append(findings, rule.newFinding(seq))
		}
	}
	return findings, true
}
