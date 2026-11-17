package dashboard

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// DetectionView is the JSON shape of one active threat rule for the dashboard.
// Rule fields carry only yaml tags, so the active rule set is mapped into this
// view rather than serialized directly.
type DetectionView struct {
	ID          string                    `json:"id"`
	Title       string                    `json:"title"`
	Description string                    `json:"description,omitempty"`
	Severity    asymptoteobserve.Severity `json:"severity"`
	Status      threatrules.Status        `json:"status"`
	Posture     threatrules.Posture       `json:"posture"`
	Kind        string                    `json:"kind"` // "match" or "correlation"
	Reason      string                    `json:"reason"`
	Source      detect.Source             `json:"source"` // baseline | store
	TestCount   int                       `json:"test_count"`
	Taxonomy    map[string]string         `json:"taxonomy,omitempty"`
}

// DetectionsResponse is the /api/detections payload.
type DetectionsResponse struct {
	Rules []DetectionView `json:"rules"`
	Count int             `json:"count"`
	// Skipped lists rule files left out because they need a newer Beacon.
	Skipped []string `json:"skipped,omitempty"`
}

// BuildDetections lists the active rule set (the store plus every embedded baseline
// rule it does not replace by id — the same set `beacon scan` would run) as detection
// views.
func BuildDetections(userMode bool, rulesDir string) (DetectionsResponse, error) {
	loaded, skipped, err := detect.LoadActiveSkipping(userMode, strings.TrimSpace(rulesDir))
	if err != nil {
		return DetectionsResponse{}, err
	}
	rules := make([]DetectionView, 0, len(loaded))
	for _, lr := range loaded {
		r := lr.Rule
		kind := "match"
		if r.Correlation != nil {
			kind = "correlation"
		}
		rules = append(rules, DetectionView{
			ID:          r.ID,
			Title:       r.Title,
			Description: r.Description,
			Severity:    r.Severity,
			Status:      r.Status,
			Posture:     r.Posture,
			Kind:        kind,
			Reason:      r.Emit.Reason,
			Source:      lr.Source,
			TestCount:   len(r.Tests),
			Taxonomy:    r.Taxonomy,
		})
	}
	return DetectionsResponse{Rules: rules, Count: len(rules), Skipped: detect.SkippedStrings(skipped)}, nil
}

// FindingsResponse is the /api/findings payload: the hits from running the
// active rules over the runtime log, plus how many events were scanned.
type FindingsResponse struct {
	Findings []threatrules.Finding `json:"findings"`
	Scanned  int                   `json:"scanned"`
	Count    int                   `json:"count"`
	// Skipped lists rule files left out because they need a newer Beacon.
	Skipped []string `json:"skipped,omitempty"`
	// Failed lists the rules the scan could not evaluate on some events (most often a command
	// line that took a rule past its cost limit). Their verdict on those events is unknown;
	// everything else was scanned.
	Failed []FailedRuleView `json:"failed,omitempty"`
}

// FailedRuleView is one rule the scan could not evaluate: on how many events, the first of them,
// and why.
type FailedRuleView struct {
	RuleID         string `json:"rule_id"`
	Events         int    `json:"events"`
	FirstEventID   string `json:"first_event_id,omitempty"`
	FirstSessionID string `json:"first_session_id,omitempty"`
	Error          string `json:"error"`
}

// RunScan loads and compiles the active rules, streams the runtime log (optionally
// filtered to one session), runs detection, and returns findings sorted highest
// severity first. It mirrors `beacon scan` and is read-only and offline. minSeverity,
// when set, drops lower-severity findings from the result (display filter only).
func RunScan(userMode bool, logPath, rulesDir, session, minSeverity string) (FindingsResponse, error) {
	minRank := 0
	if s := strings.TrimSpace(minSeverity); s != "" {
		sev := asymptoteobserve.Severity(s)
		switch sev {
		case asymptoteobserve.SeverityInfo, asymptoteobserve.SeverityLow, asymptoteobserve.SeverityMedium,
			asymptoteobserve.SeverityHigh, asymptoteobserve.SeverityCritical:
			minRank = threatrules.SeverityRank(sev)
		default:
			return FindingsResponse{}, fmt.Errorf("invalid min_severity %q (info|low|medium|high|critical)", s)
		}
	}

	compiled, skipped, err := compileActiveRules(userMode, rulesDir)
	if err != nil {
		return FindingsResponse{}, err
	}

	sessionFilter := strings.TrimSpace(session)
	var events []asymptoteobserve.Event
	err = StreamEvents(logPath, func(e schema.Event) error {
		if sessionFilter != "" && (e.Session == nil || !strings.Contains(strings.ToLower(e.Session.ID), strings.ToLower(sessionFilter))) {
			return nil
		}
		events = append(events, e)
		return nil
	})
	if err != nil {
		return FindingsResponse{}, fmt.Errorf("read telemetry %s: %w", logPath, err)
	}

	// The rules engine evaluates the sequence it is handed, and the log is in append
	// order, which is not the order the events happened: a hook writes the instant it
	// intercepts a tool call while the exporter writes on its export interval. Ordered
	// correlation rules silently miss without this.
	threatrules.SortEvents(events)

	result := threatrules.Scan(compiled, events)
	findings := result.Findings
	if minRank > 0 {
		findings = threatrules.FilterBySeverity(findings, minRank)
	}
	threatrules.SortFindings(findings)
	if findings == nil {
		findings = []threatrules.Finding{}
	}
	var failed []FailedRuleView
	for _, f := range threatrules.FailuresByRule(result.Failures) {
		failed = append(failed, FailedRuleView{RuleID: f.RuleID, Events: f.Events, FirstEventID: f.First.EventID, FirstSessionID: f.First.SessionID, Error: f.First.Err.Error()})
	}
	return FindingsResponse{Findings: findings, Scanned: len(events), Count: len(findings), Skipped: skipped, Failed: failed}, nil
}

// compileActiveRules loads and compiles the rules `beacon scan` would run. It returns a
// *detect.NoRulesError when none are active, so callers can tell "no rules" from a broken rule.
func compileActiveRules(userMode bool, rulesDir string) ([]*threatrules.CompiledRule, []string, error) {
	loaded, skipped, err := detect.LoadActiveSkipping(userMode, strings.TrimSpace(rulesDir))
	if err != nil {
		return nil, nil, fmt.Errorf("load rules: %w", err)
	}
	if len(loaded) == 0 {
		return nil, nil, &detect.NoRulesError{Skipped: skipped}
	}
	compiled := make([]*threatrules.CompiledRule, 0, len(loaded))
	for _, lr := range loaded {
		c, err := threatrules.Compile(lr.Rule)
		if err != nil {
			return nil, nil, fmt.Errorf("compile rule %q: %w", lr.Rule.ID, err)
		}
		compiled = append(compiled, c)
	}
	return compiled, detect.SkippedStrings(skipped), nil
}
