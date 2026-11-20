package detect

import (
	"fmt"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// ScanReport is one scan of a log with the active rules: what they found, and what they did not
// judge. A scan is complete only when Skipped and Failed are both empty; every surface that shows
// findings shows those two as well, or it would call an unjudged event clean.
type ScanReport struct {
	// Rules are the compiled active rules, in load order.
	Rules []*threatrules.CompiledRule
	// Skipped are the rules set aside because they need a newer spec than this binary supports.
	Skipped []threatrules.SkippedRule
	// Scanned is how many events the rules ran over.
	Scanned int
	// Findings are highest severity first, then by rule id.
	Findings []threatrules.Finding
	// Failed are the rules the scan could not evaluate on some events, in the order each first
	// failed. Their verdict on those events is unknown.
	Failed []threatrules.RuleFailures
}

// Scan is the scan `beacon scan`, the dashboard's findings page and a lens's findings run: it
// loads and compiles the active rules, then reads the events with read and runs the rules over
// them. The rules are compiled first, so a broken store is reported before a large log is read.
// It returns a *NoRulesError when no rule is active.
//
// An event is judged and reported under its own ID; Scan gives none to an event without one. A
// caller whose output cites events by another handle (a lens, whose evidence refers to
// trace.events[].id) sets that handle on the copies read returns.
//
// read's slice is reordered in place.
func Scan(userMode bool, rulesDir string, read func() ([]asymptoteobserve.Event, error)) (ScanReport, error) {
	loaded, skipped, err := LoadActiveSkipping(userMode, strings.TrimSpace(rulesDir))
	if err != nil {
		return ScanReport{}, fmt.Errorf("load rules: %w", err)
	}
	if len(loaded) == 0 {
		return ScanReport{}, &NoRulesError{Skipped: skipped}
	}
	rules := make([]*threatrules.CompiledRule, 0, len(loaded))
	for _, lr := range loaded {
		c, err := threatrules.Compile(lr.Rule)
		if err != nil {
			return ScanReport{}, fmt.Errorf("compile rule %q: %w", lr.Rule.ID, err)
		}
		rules = append(rules, c)
	}
	events, err := read()
	if err != nil {
		return ScanReport{}, err
	}
	// The rules engine evaluates the sequence it is handed, and the log is in append order, which
	// is not the order the events happened: a hook writes the instant it intercepts a tool call
	// while the exporter writes on its export interval. Ordered correlation rules silently miss
	// without this.
	threatrules.SortEvents(events)
	result := threatrules.Scan(rules, events)
	threatrules.SortFindings(result.Findings)
	return ScanReport{
		Rules:    rules,
		Skipped:  skipped,
		Scanned:  len(events),
		Findings: result.Findings,
		Failed:   threatrules.FailuresByRule(result.Failures),
	}, nil
}

// ParseSeverity reads a severity threshold (--min-severity, --fail-on, min_severity) as a rank
// comparable with threatrules.SeverityRank. ok is false for anything but the five severities.
func ParseSeverity(s string) (rank int, ok bool) {
	switch sev := asymptoteobserve.Severity(strings.TrimSpace(s)); sev {
	case asymptoteobserve.SeverityInfo, asymptoteobserve.SeverityLow, asymptoteobserve.SeverityMedium,
		asymptoteobserve.SeverityHigh, asymptoteobserve.SeverityCritical:
		return threatrules.SeverityRank(sev), true
	}
	return 0, false
}
