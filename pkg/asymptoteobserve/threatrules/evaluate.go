package threatrules

import (
	"fmt"
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Every path that evaluates an expression on an event goes through expressions.evaluate: Scan and
// ScanEvents, an EventScanner, a StepMatcher, the step table of a scan's correlation rules, a
// snapshot (TakeSnapshot) and EvalMatch. It decides the three things each path once decided for
// itself: which expressions an event's action leaves to evaluate, which form of the event each
// reads, and how an evaluation that fails reaches the caller. What a caller does with a failure
// (record it and go on, or stop) is the caller's, and each says so where it hands its answer.

// expressions are compiled expressions, and which of them can be true for an event of each action.
type expressions struct {
	match []matcher
	index actionIndex
}

func newExpressions(match []matcher) expressions {
	return expressions{match: match, index: indexActions(match)}
}

// actionIndex lists, for each action some expression of a set is limited to (eventActions), the
// expressions that can be true for an event with that action, in order; any lists them for every
// other action.
type actionIndex struct {
	by  map[string][]int
	any []int
}

func indexActions(match []matcher) actionIndex {
	x := actionIndex{by: map[string][]int{}}
	for i := range match {
		if match[i].actions == nil {
			x.any = append(x.any, i)
		}
	}
	for i := range match {
		for action := range match[i].actions {
			if _, ok := x.by[action]; !ok {
				x.by[action] = slices.Clone(x.any)
			}
			x.by[action] = append(x.by[action], i)
		}
	}
	for _, is := range x.by {
		slices.Sort(is)
	}
	return x
}

// of returns the expressions that can be true for an event with the action, in order.
func (x *actionIndex) of(action string) []int {
	if is, ok := x.by[action]; ok {
		return is
	}
	return x.any
}

// evalObserver, when set, is called once per expression evaluation. It is nil outside tests,
// which set it to count how much work a rule does without timing it.
var evalObserver func()

// evaluate evaluates, in order, each expression that can be true for the action of the event in
// (the rest are false having evaluated nothing that could fail: eventActions) and that ask wants,
// every one when ask is nil. Each reads the event in the form it is compiled for (matcher.derived),
// through in, so an event's derived fields are worked out once for every expression that reads
// them. Each answer goes to answer: whether the expression held, or the error that stopped it, in
// which case it did not hold. answer returns whether to go on; evaluate reports whether it did
// until the last expression.
func (x *expressions) evaluate(in *eventInput, ask func(i int) bool, answer func(i int, held bool, err error) bool) bool {
	for _, i := range x.index.of(in.event.Event.Action) {
		if ask != nil && !ask(i) {
			continue
		}
		m := &x.match[i]
		act := in.activation(m.derived)
		if evalObserver != nil {
			evalObserver()
		}
		held, err := evalMatch(m.program(act), act)
		if !answer(i, held, err) {
			return false
		}
	}
	return true
}

// evalAlone evaluates one expression on an event of its own.
func evalAlone(m matcher, event asymptoteobserve.Event) (held bool, err error) {
	x := newExpressions([]matcher{m})
	x.evaluate(&eventInput{event: &event}, nil, func(_ int, h bool, e error) bool {
		held, err = h, e
		return true
	})
	return held, err
}

// ruleError names the rule an evaluation failed in: an evaluation error belongs to the rule, not to
// the log, and the operator needs to know which to remove.
func ruleError(ruleID string, err error) error {
	return fmt.Errorf("rule %q: %w", ruleID, err)
}
