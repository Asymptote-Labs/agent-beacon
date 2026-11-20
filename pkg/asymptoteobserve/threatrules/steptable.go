package threatrules

import (
	"fmt"
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// stepTable holds, for the correlation rules of one scan, whether each of their steps holds for
// each event. It is filled while the scan passes over the events, from the activation the
// single-event rules read the event through, so an event's derived fields are worked out once
// for the whole scan rather than once for every step that is asked about the event, and a step
// several rules share (the credential read of the read-then-egress rules) is evaluated once.
//
// It has two phases. While the scan passes over the events, fill evaluates on each, in order,
// the steps a matcher can ask about it: the first step of a sequence and every step of an
// order: any rule on every event, a later step of a sequence on the events that follow, in
// their session, one the step before it holds for (nextHit looks nowhere else). Once every
// event is filled, each rule's matcher asks the table instead of evaluating (findings), in
// the order it always did, so which events match and which evidence a finding carries are
// what each rule finds on its own. An evaluation that failed is reported only when the
// matcher asks for that step on that event, and the step does not hold there.
//
// A cell a matcher asks and fill did not evaluate, on an event whose action the step can be
// true for, would mean fill and the matcher disagree about what can be asked; holds panics on
// it rather than answer for a step it never evaluated.
type stepTable struct {
	events  []asymptoteobserve.Event
	columns []stepColumn
	steps   expressions             // the columns' expressions
	of      map[*CompiledRule][]int // a rule's steps, as columns
	// answers holds each column's answer on each event, by its index; failed the error of
	// each answer that is stepFailed.
	answers [][]stepAnswer
	failed  map[[2]int]error // (column, event)
	// sessions are the events of each session, in the order the sessions first appear, and
	// sessionOf the number of each event's session.
	sessions  []sessionEvents
	sessionOf []int
	// held has, for each session, a flag per column: a feeding column that has held for an
	// event of the session filled so far. filled is how many events are.
	held   [][]bool
	filled int
}

// stepAnswer is what the table knows of one step on one event.
type stepAnswer uint8

const (
	// stepNotAsked: not evaluated, because the event's action rules the step out, or because
	// no matcher can ask the step there.
	stepNotAsked stepAnswer = iota
	stepHolds
	stepDoesNotHold
	// stepFailed: the evaluation failed (most often at MaxEvalCost); its error is in failed.
	stepFailed
)

// stepColumn is when a matcher can ask one distinct step.
type stepColumn struct {
	// always is set when the step is asked on every event; otherwise it is asked on the events
	// after one that a column in after holds for. feeds is set on a column listed in an after.
	always bool
	after  []int
	feeds  bool
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
	t := &stepTable{events: events, of: of, steps: newExpressions(stepMatchers(steps)), columns: make([]stepColumn, len(steps))}
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
	t.answers = make([][]stepAnswer, len(t.columns))
	for c := range t.answers {
		t.answers[c] = make([]stepAnswer, len(events))
	}
	t.sessions, t.sessionOf = groupSessions(events)
	t.held = make([][]bool, len(t.sessions))
	flags := make([]bool, len(t.sessions)*len(t.columns))
	for k := range t.held {
		t.held[k] = flags[k*len(t.columns) : (k+1)*len(t.columns)]
	}
	return t
}

// fill evaluates on event i, read through in, the steps a matcher can ask about it. The events
// are filled in order, each once.
func (t *stepTable) fill(i int, in *eventInput) {
	if i != t.filled {
		panic(fmt.Sprintf("stepTable: event %d filled after %d", i, t.filled))
	}
	t.filled++
	held := t.held[t.sessionOf[i]] // as it was before this event
	var fed []int
	t.steps.evaluate(in, func(c int) bool {
		col := &t.columns[c]
		return col.always || slices.ContainsFunc(col.after, func(before int) bool { return held[before] })
	}, func(c int, holds bool, err error) bool {
		// Every answer is kept, a failure with its error: it is reported if a matcher asks.
		switch {
		case err != nil:
			t.answers[c][i] = stepFailed
			if t.failed == nil {
				t.failed = map[[2]int]error{}
			}
			t.failed[[2]int{c, i}] = err
		case holds:
			t.answers[c][i] = stepHolds
			if t.columns[c].feeds {
				fed = append(fed, c)
			}
		default:
			t.answers[c][i] = stepDoesNotHold
		}
		return true
	})
	for _, c := range fed {
		held[c] = true
	}
}

// holds reports whether column c holds for event i, as evaluating the step on the event does,
// and the error of an evaluation that failed.
func (t *stepTable) holds(c, i int) (bool, error) {
	switch t.answers[c][i] {
	case stepHolds:
		return true, nil
	case stepDoesNotHold:
		return false, nil
	case stepFailed:
		return false, t.failed[[2]int{c, i}]
	}
	if t.steps.match[c].excludes(t.events[i].Event.Action) {
		return false, nil
	}
	panic(fmt.Sprintf("stepTable: step %d asked on event %d, which fill did not evaluate it on", c, i))
}

// findings returns what the rule finds in the scanned events, asking the table for every step. A
// step that could not be evaluated on an event is handed to fail, with the event's index in the
// scan, once for each event, and is taken not to hold there: the rule goes on, and finds what it
// can from the evaluations that did not fail, unless fail says to stop, which findings reports.
func (t *stepTable) findings(rule *CompiledRule, fail func(event int, err error) bool) ([]Finding, bool) {
	if t.filled != len(t.events) {
		panic(fmt.Sprintf("stepTable: findings asked with %d of %d events filled", t.filled, len(t.events)))
	}
	columns := t.of[rule]
	var findings []Finding
	var reported map[int]bool
	stopped := false
	for k := range t.sessions {
		session := &t.sessions[k]
		seq := rule.matchSession(session, func(step, event int) bool {
			if stopped {
				return false
			}
			i := session.at[event]
			held, err := t.holds(columns[step], i)
			if err != nil && !reported[i] {
				if reported == nil {
					reported = map[int]bool{}
				}
				reported[i] = true
				stopped = !fail(i, err)
			}
			return held
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
