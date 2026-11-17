package threatrules

import (
	"fmt"
)

// StepMatcher reports whether an event matches any step of a set of correlation rules: whether a
// caller that keeps events for those rules needs to keep this one. A step several rules share
// (distinctSteps) is evaluated once. It is built once and is safe for concurrent use.
type StepMatcher struct {
	steps []distinctStep
	match expressions // the steps' expressions
}

// NewStepMatcher returns a matcher over the steps of correlation rules, in rule and step order.
// A single-event rule has no steps and is rejected.
func NewStepMatcher(rules []*CompiledRule) (*StepMatcher, error) {
	for _, r := range rules {
		if r.steps == nil {
			return nil, fmt.Errorf("rule %q is a single-event rule; a step matcher runs correlation steps", r.rule.ID)
		}
	}
	steps, _ := distinctSteps(rules)
	return &StepMatcher{steps: steps, match: newExpressions(stepMatchers(steps))}, nil
}

// AnyStep reports whether some step's match holds for the event, stopping at the first that does.
// A step whose evaluation fails does not hide a later one that matches: the result is true when any
// step evaluates to true, as each rule's own evaluation would find it. Only when none does is the
// first failure returned, naming its rule and step.
//
// That is its failure policy, and the reason it does not stop at the first failure as ScanPrepared
// does: the question is whether to keep the event, and a failed step says nothing about whether
// another step needs it.
func (m *StepMatcher) AnyStep(p *PreparedEvent) (bool, error) {
	var first error
	holds := false
	m.match.evaluate(&p.in, nil, func(i int, held bool, err error) bool {
		switch {
		case err != nil:
			if first == nil {
				st := &m.steps[i]
				first = fmt.Errorf("rule %q step %q: %w", st.rule.rule.ID, st.rule.rule.Correlation.Steps[st.step].ID, err)
			}
			return true
		case held:
			holds = true
			return false
		}
		return true
	})
	if holds {
		return true, nil
	}
	return false, first
}
