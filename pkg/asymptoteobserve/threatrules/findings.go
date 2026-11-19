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
		seq, err := c.matchSession(groups[sid], c.ownSteps(groups[sid]))
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

// ScanFailure is one evaluation a scan could not finish: a rule on an event, most often stopped
// at MaxEvalCost. The rule's verdict on that event is unknown, neither a match nor a no-match
// (SPEC "Evaluation is bounded"). For a correlation rule it is a step on the event, which the
// rule then takes not to hold there.
type ScanFailure struct {
	RuleID    string
	EventID   string
	SessionID string
	Err       error
}

// ScanResult is what a scan found, and the evaluations it could not finish.
type ScanResult struct {
	Findings []Finding
	Failures []ScanFailure
}

// RuleFailures is what one rule could not evaluate in a scan: on how many events, and the first
// of them. A hostile log can fail one rule on every event, so callers report failures by rule.
type RuleFailures struct {
	RuleID string
	Events int
	First  ScanFailure
}

// FailuresByRule groups failures by rule, in the order each rule first failed.
func FailuresByRule(failures []ScanFailure) []RuleFailures {
	var out []RuleFailures
	at := map[string]int{}
	for _, f := range failures {
		k, ok := at[f.RuleID]
		if !ok {
			k = len(out)
			at[f.RuleID] = k
			out = append(out, RuleFailures{RuleID: f.RuleID, First: f})
		}
		out[k].Events++
	}
	return out
}

// Scan runs every compiled rule over the events and returns all findings, in rule order, and
// every evaluation that failed. It is what `beacon scan` and the dashboard run.
//
// An evaluation that fails is a failure of that rule on that event and nothing more: the other
// rules still judge the event and the rule still judges the other events. One command line an
// agent was made to run can reach the cost limit of a rule, and a scan that stopped there would
// report nothing about any session in the log.
//
// The rules run event by event, as an EventScanner runs the single-event ones: only the rules
// that can match an event's action are evaluated, and an event's derived fields (a parsed
// command line, a tool result's text) are computed once for every rule that references them,
// not once per rule, and dropped before the next event. The steps of the correlation rules are
// evaluated in the same pass, on the same form of the event, and each rule is then matched
// over the whole sequence from those answers (stepTable).
func Scan(rules []*CompiledRule, events []asymptoteobserve.Event) ScanResult {
	var result ScanResult
	failure := func(rule *CompiledRule, e *asymptoteobserve.Event, err error) {
		result.Failures = append(result.Failures, ScanFailure{RuleID: rule.rule.ID, EventID: e.Event.ID, SessionID: sessionID(e), Err: err})
	}
	found := make([][]Finding, len(rules))
	var single []*CompiledRule
	var at []int // at[k] is the position of single[k] in rules
	for r, rule := range rules {
		if rule.steps == nil {
			single = append(single, rule)
			at = append(at, r)
		}
	}
	steps := newStepTable(rules, events)
	if len(single) > 0 || steps != nil {
		scanner := newEventScanner(single)
		for i := range events {
			in := &eventInput{event: &events[i]}
			scanner.scan(in, func(k int) {
				found[at[k]] = append(found[at[k]], single[k].newFinding([]asymptoteobserve.Event{events[i]}))
			}, func(k int, err error) {
				failure(single[k], &events[i], err)
			})
			if steps != nil {
				steps.fill(i, in)
			}
		}
	}
	for r, rule := range rules {
		if rule.steps != nil {
			found[r] = steps.findings(rule, func(i int, err error) { failure(rule, &events[i], err) })
		}
		result.Findings = append(result.Findings, found[r]...)
	}
	return result
}

// ScanEvents is Scan for a caller that must not act on an incomplete scan: an evaluation that
// fails fails the scan, and the error names the rule. With several, it is the first a single-event
// rule met in the order of the events, or else the first of the correlation rules in rule order.
func ScanEvents(rules []*CompiledRule, events []asymptoteobserve.Event) ([]Finding, error) {
	result := Scan(rules, events)
	if len(result.Failures) > 0 {
		f := result.Failures[0]
		// Name the rule: an evaluation error belongs to the rule, not to the log, and the
		// operator needs to know which to remove.
		return nil, fmt.Errorf("rule %q: %w", f.RuleID, f.Err)
	}
	return result.Findings, nil
}
