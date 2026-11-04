package threatrules

import (
	"fmt"
	"sort"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// severityRank orders severities for filtering and sorting findings.
var severityRank = map[asymptoteobserve.Severity]int{
	asymptoteobserve.SeverityInfo:     0,
	asymptoteobserve.SeverityLow:      1,
	asymptoteobserve.SeverityMedium:   2,
	asymptoteobserve.SeverityHigh:     3,
	asymptoteobserve.SeverityCritical: 4,
}

// SeverityRank returns the comparable rank of a severity (info=0 … critical=4).
// Unknown values rank as 0. It is the single ordering shared by the `beacon scan`
// command and the dashboard findings view.
func SeverityRank(s asymptoteobserve.Severity) int { return severityRank[s] }

// SortFindings orders findings highest-severity first, then by rule id, in place.
func SortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := severityRank[findings[i].Severity], severityRank[findings[j].Severity]
		if ri != rj {
			return ri > rj
		}
		return findings[i].RuleID < findings[j].RuleID
	})
}

// FilterBySeverity returns the findings at or above minRank (see SeverityRank).
func FilterBySeverity(findings []Finding, minRank int) []Finding {
	out := findings[:0]
	for _, f := range findings {
		if severityRank[f.Severity] >= minRank {
			out = append(out, f)
		}
	}
	return out
}

// CountAtOrAbove returns how many findings are at or above rank.
func CountAtOrAbove(findings []Finding, rank int) int {
	n := 0
	for _, f := range findings {
		if severityRank[f.Severity] >= rank {
			n++
		}
	}
	return n
}

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
	// matched step events for a correlation rule (in step order for a sequence, in the
	// order they happened for order: any).
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
		matched, err := c.eval(c.match, events[i])
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
//
// Single-event rules run event by event, so an event's derived fields (a parsed command line,
// a tool result's text) are computed once for every rule that references them, not once per
// rule, and dropped before the next event. Correlation rules run after, each over the whole
// sequence.
func ScanEvents(rules []*CompiledRule, events []asymptoteobserve.Event) ([]Finding, error) {
	found := make([][]Finding, len(rules))
	for i := range events {
		var derived asymptoteobserve.Event
		haveDerived := false
		for r, rule := range rules {
			if rule.steps != nil {
				continue
			}
			event := &events[i]
			if rule.derived {
				if !haveDerived {
					derived, haveDerived = withDerivedFields(events[i]), true
				}
				event = &derived
			}
			matched, err := evalDerived(rule.match, *event)
			if err != nil {
				// Name the rule: an evaluation error (such as one that exceeded MaxEvalCost)
				// belongs to the rule, not to the log, and the operator needs to know which to
				// remove.
				return nil, fmt.Errorf("rule %q: %w", rule.Rule().ID, err)
			}
			if matched {
				found[r] = append(found[r], rule.newFinding([]asymptoteobserve.Event{events[i]}))
			}
		}
	}
	var all []Finding
	for r, rule := range rules {
		if rule.steps != nil {
			f, err := rule.correlationFindings(events)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", rule.Rule().ID, err)
			}
			found[r] = f
		}
		all = append(all, found[r]...)
	}
	return all, nil
}
