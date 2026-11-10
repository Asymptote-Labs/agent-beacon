package threatrules

import (
	"fmt"
	"slices"
)

// StepMatcher reports whether an event matches any step of a set of correlation rules: whether a
// caller that keeps events for those rules needs to keep this one. Steps with the same expression,
// read on the same form of the event, are evaluated once. It is built once and is safe for
// concurrent use.
type StepMatcher struct {
	steps     []step
	byAction  map[string][]int // as EventScanner's, over steps
	anyAction []int
}

type step struct {
	match   matcher
	derived bool // the step's rule reads derived fields, so the step reads the derived event
	name    string
}

// NewStepMatcher returns a matcher over the steps of correlation rules, in rule and step order.
// A single-event rule has no steps and is rejected.
func NewStepMatcher(rules []*CompiledRule) (*StepMatcher, error) {
	m := &StepMatcher{byAction: map[string][]int{}}
	seen := map[[2]any]bool{}
	for _, r := range rules {
		if r.steps == nil {
			return nil, fmt.Errorf("rule %q is a single-event rule; a step matcher runs correlation steps", r.rule.ID)
		}
		for i, st := range r.rule.Correlation.Steps {
			key := [2]any{st.Match, r.derived}
			if seen[key] {
				continue
			}
			seen[key] = true
			m.steps = append(m.steps, step{match: r.steps[i], derived: r.derived, name: fmt.Sprintf("rule %q step %q", r.rule.ID, st.ID)})
		}
	}
	for i, st := range m.steps {
		if st.match.actions == nil {
			m.anyAction = append(m.anyAction, i)
		}
	}
	for i, st := range m.steps {
		for action := range st.match.actions {
			if _, ok := m.byAction[action]; !ok {
				m.byAction[action] = slices.Clone(m.anyAction)
			}
			m.byAction[action] = append(m.byAction[action], i)
		}
	}
	for _, ss := range m.byAction {
		slices.Sort(ss)
	}
	return m, nil
}

// AnyStep reports whether some step's match holds for the event, stopping at the first that does.
// A step whose evaluation fails does not hide a later one that matches: the result is true when any
// step evaluates to true, as each rule's own evaluation would find it. Only when none does is the
// first failure returned, naming its rule and step.
func (m *StepMatcher) AnyStep(p *PreparedEvent) (bool, error) {
	candidates, ok := m.byAction[p.event.Event.Action]
	if !ok {
		candidates = m.anyAction
	}
	var first error
	for _, i := range candidates {
		st := &m.steps[i]
		act := p.in.activation(st.derived)
		matched, err := evalObserved(st.match.program(act), act)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("%s: %w", st.name, err)
			}
			continue
		}
		if matched {
			return true, nil
		}
	}
	return false, first
}
