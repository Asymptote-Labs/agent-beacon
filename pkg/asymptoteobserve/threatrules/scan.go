package threatrules

import (
	"fmt"
	"slices"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// EventScanner runs single-event rules against one event at a time, for a caller that judges
// events as they arrive rather than scanning a log. It is built once and is safe for concurrent
// use.
//
// For each event it evaluates only the rules that can match the event's action (see
// eventActions), and every rule reads the event through one activation, so the event's derived
// fields are worked out once, and only when a rule that reads them is evaluated.
type EventScanner struct {
	rules []*CompiledRule
	// byAction lists, for each action some rule is limited to, the rules that can match an event
	// with that action, in rule order; anyAction lists them for every other action.
	byAction  map[string][]int
	anyAction []int
}

// NewEventScanner returns a scanner over single-event rules. A correlation rule needs the events
// of a session and is rejected.
func NewEventScanner(rules []*CompiledRule) (*EventScanner, error) {
	for _, r := range rules {
		if r.steps != nil {
			return nil, fmt.Errorf("rule %q is a correlation rule; an event scanner runs single-event rules", r.rule.ID)
		}
	}
	return newEventScanner(rules), nil
}

func newEventScanner(rules []*CompiledRule) *EventScanner {
	s := &EventScanner{rules: rules, byAction: map[string][]int{}}
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

// ScanEvent returns a finding for every rule that matches the event, in rule order, with the event
// as evidence: what ScanEvents returns for the same rules and this one event. An evaluation error
// (such as one that exceeded MaxEvalCost) fails the scan and names the rule.
func (s *EventScanner) ScanEvent(event asymptoteobserve.Event) ([]Finding, error) {
	return s.ScanPrepared(PrepareEvent(event))
}

// ScanPrepared is ScanEvent for a prepared event, whose activations, derived fields and size tier
// are then shared with whatever else evaluates it (StepMatcher.AnyStep).
func (s *EventScanner) ScanPrepared(p *PreparedEvent) ([]Finding, error) {
	var findings []Finding
	err := s.scan(&p.in, func(r int) {
		findings = append(findings, s.rules[r].newFinding([]asymptoteobserve.Event{p.event}))
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

// PreparedEvent is one event ready for evaluation by an EventScanner and a StepMatcher. What
// evaluating it needs (its binding, its derived fields, the size tier that decides untracked
// evaluation, the canonical text prefilters search) is worked out on first use and shared by every
// rule and step evaluated on it. It is not safe for concurrent use.
type PreparedEvent struct {
	event asymptoteobserve.Event
	in    eventInput
}

// PrepareEvent prepares an event for evaluation; nothing is computed until a rule needs it.
func PrepareEvent(event asymptoteobserve.Event) *PreparedEvent {
	p := &PreparedEvent{event: event}
	p.in.event = &p.event
	return p
}

// scan calls hit with the index of every rule that matches the event, in rule order.
func (s *EventScanner) scan(in *eventInput, hit func(rule int)) error {
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
