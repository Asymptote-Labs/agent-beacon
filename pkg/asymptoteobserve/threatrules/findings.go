package threatrules

import "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"

// Finding is a rule match with its supporting evidence, produced when evaluating a rule
// over a real event stream (as opposed to the boolean Verdict used for conformance).
type Finding struct {
	RuleID    string                    `json:"rule_id"`
	Title     string                    `json:"title"`
	Severity  asymptoteobserve.Severity `json:"severity"`
	Posture   Posture                   `json:"posture"`
	SessionID string                    `json:"session_id,omitempty"`
	Reason    string                    `json:"reason"`
	// Events is the evidence: the single matching event for a single-event rule, or the
	// matched step events (in order) for a correlation rule.
	Events []asymptoteobserve.Event `json:"events"`
}

// Findings returns every match of the compiled rule over the given events, with evidence.
//
//   - Single-event rule: one Finding per event that satisfies the match expression.
//   - Correlation rule: one Finding per session whose ordered events satisfy the
//     sequence within the window; Events holds the matched step events.
func (c *CompiledRule) Findings(events []asymptoteobserve.Event) ([]Finding, error) {
	if c.steps != nil {
		return c.correlationFindings(events)
	}
	var findings []Finding
	for i := range events {
		matched, err := EvalMatch(c.match, events[i])
		if err != nil {
			return nil, err
		}
		if matched {
			findings = append(findings, c.newFinding([]asymptoteobserve.Event{events[i]}))
		}
	}
	return findings, nil
}

func (c *CompiledRule) correlationFindings(events []asymptoteobserve.Event) ([]Finding, error) {
	groups, order := groupBySession(events)
	var findings []Finding
	for _, sid := range order {
		seq, err := c.matchSession(groups[sid])
		if err != nil {
			return nil, err
		}
		if seq != nil {
			findings = append(findings, c.newFinding(seq))
		}
	}
	return findings, nil
}

func (c *CompiledRule) newFinding(evidence []asymptoteobserve.Event) Finding {
	sessionID := ""
	if len(evidence) > 0 && evidence[0].Session != nil {
		sessionID = evidence[0].Session.ID
	}
	return Finding{
		RuleID:    c.rule.ID,
		Title:     c.rule.Title,
		Severity:  c.rule.Severity,
		Posture:   c.rule.Posture,
		SessionID: sessionID,
		Reason:    c.rule.Emit.Reason,
		Events:    evidence,
	}
}

// ScanEvents runs every compiled rule over the events and returns all findings, in rule
// order. It is the aggregator backing the `beacon scan` command.
func ScanEvents(rules []*CompiledRule, events []asymptoteobserve.Event) ([]Finding, error) {
	var all []Finding
	for _, rule := range rules {
		found, err := rule.Findings(events)
		if err != nil {
			return nil, err
		}
		all = append(all, found...)
	}
	return all, nil
}
