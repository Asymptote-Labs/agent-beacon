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
}

// BuildDetections lists the active rule set (store when present, else the
// embedded baseline — the same set `beacon scan` would run) as detection views.
func BuildDetections(userMode bool, rulesDir string) (DetectionsResponse, error) {
	loaded, err := detect.LoadActive(userMode, strings.TrimSpace(rulesDir))
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
	return DetectionsResponse{Rules: rules, Count: len(rules)}, nil
}

// FindingsResponse is the /api/findings payload: the hits from running the
// active rules over the runtime log, plus how many events were scanned.
type FindingsResponse struct {
	Findings []threatrules.Finding `json:"findings"`
	Scanned  int                   `json:"scanned"`
	Count    int                   `json:"count"`
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

	loaded, err := detect.LoadActive(userMode, strings.TrimSpace(rulesDir))
	if err != nil {
		return FindingsResponse{}, fmt.Errorf("load rules: %w", err)
	}
	if len(loaded) == 0 {
		return FindingsResponse{}, fmt.Errorf("no rules to run (store is empty and baseline missing)")
	}
	compiled := make([]*threatrules.CompiledRule, 0, len(loaded))
	for _, lr := range loaded {
		c, err := threatrules.Compile(lr.Rule)
		if err != nil {
			return FindingsResponse{}, fmt.Errorf("compile rule %q: %w", lr.Rule.ID, err)
		}
		compiled = append(compiled, c)
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

	findings, err := threatrules.ScanEvents(compiled, events)
	if err != nil {
		return FindingsResponse{}, fmt.Errorf("scan: %w", err)
	}
	if minRank > 0 {
		findings = threatrules.FilterBySeverity(findings, minRank)
	}
	threatrules.SortFindings(findings)
	if findings == nil {
		findings = []threatrules.Finding{}
	}
	return FindingsResponse{Findings: findings, Scanned: len(events), Count: len(findings)}, nil
}
