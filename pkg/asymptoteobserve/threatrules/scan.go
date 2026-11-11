package threatrules

import (
	"fmt"
	"slices"
)

// eventScanner runs single-event rules against one event at a time. It is built once and is safe
// for concurrent use.
//
// For each event it evaluates only the rules that can match the event's action (see
// eventActions), and every rule reads the event through one activation, so the event's derived
// fields are worked out once, and only when a rule that reads them is evaluated.
type eventScanner struct {
	rules []*CompiledRule
	// byAction lists, for each action some rule is limited to, the rules that can match an event
	// with that action, in rule order; anyAction lists them for every other action.
	byAction  map[string][]int
	anyAction []int
}

func newEventScanner(rules []*CompiledRule) *eventScanner {
	s := &eventScanner{rules: rules, byAction: map[string][]int{}}
	for i, r := range rules {
		if r.match.actions == nil {
			s.anyAction = append(s.anyAction, i)
		}
	}
	for i, r := range rules {
		for action := range r.match.actions {
			if _, ok := s.byAction[action]; !ok {
				s.byAction[action] = slices.Clone(s.anyAction)
			}
			s.byAction[action] = append(s.byAction[action], i)
		}
	}
	for _, rs := range s.byAction {
		slices.Sort(rs)
	}
	return s
}

// scan calls hit with the index of every rule that matches the event, in rule order. An
// evaluation error (such as one that exceeded MaxEvalCost) fails the scan and names the rule.
func (s *eventScanner) scan(in *eventInput, hit func(rule int)) error {
	candidates, ok := s.byAction[in.event.Event.Action]
	if !ok {
		candidates = s.anyAction
	}
	for _, r := range candidates {
		rule := s.rules[r]
		act := in.activation(rule.derived)
		matched, err := evalObserved(rule.match.program(act), act)
		if err != nil {
			// Name the rule: an evaluation error belongs to the rule, not to the log, and the
			// operator needs to know which to remove.
			return fmt.Errorf("rule %q: %w", rule.rule.ID, err)
		}
		if matched {
			hit(r)
		}
	}
	return nil
}
