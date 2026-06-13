package threatrules

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestSingleEventFindings(t *testing.T) {
	c, err := Compile(validSingleEventRule()) // matches e.event.action == "file.read"
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	events := []asymptoteobserve.Event{
		{Event: asymptoteobserve.EventInfo{Action: "file.read"}, Session: &asymptoteobserve.SessionInfo{ID: "s1"}},
		{Event: asymptoteobserve.EventInfo{Action: "tool.invoked"}},
		{Event: asymptoteobserve.EventInfo{Action: "file.read"}, Session: &asymptoteobserve.SessionInfo{ID: "s2"}},
	}
	findings, err := c.Findings(events)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (one per matching event), got %d", len(findings))
	}
	if findings[0].RuleID != "test-rule" || findings[0].SessionID != "s1" {
		t.Fatalf("unexpected finding[0]: %+v", findings[0])
	}
	if findings[1].SessionID != "s2" {
		t.Fatalf("unexpected finding[1] session: %+v", findings[1])
	}
	if len(findings[0].Events) != 1 {
		t.Fatalf("single-event finding should carry 1 evidence event, got %d", len(findings[0].Events))
	}
	if findings[0].Reason != "test" || findings[0].Severity != asymptoteobserve.SeverityMedium {
		t.Fatalf("finding metadata not populated from rule: %+v", findings[0])
	}
}

func TestCorrelationFindingsEvidence(t *testing.T) {
	c := readThenEgressRule(t) // read .env -> curl, window 120s, session scope
	events := []asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
		// a second, unrelated session that does not complete
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s2", withEnv),
	}
	findings, err := c.Findings(events)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding (only s1 completes), got %d", len(findings))
	}
	f := findings[0]
	if f.SessionID != "s1" {
		t.Fatalf("want session s1, got %q", f.SessionID)
	}
	if len(f.Events) != 2 {
		t.Fatalf("correlation finding should carry both step events, got %d", len(f.Events))
	}
	if f.Events[0].Event.Action != "file.read" || f.Events[1].Event.Action != "command.executed" {
		t.Fatalf("evidence steps out of order: %+v", f.Events)
	}
}

func TestScanEventsAggregates(t *testing.T) {
	single, err := Compile(validSingleEventRule())
	if err != nil {
		t.Fatalf("compile single: %v", err)
	}
	corr := readThenEgressRule(t)
	events := []asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
	}
	findings, err := ScanEvents([]*CompiledRule{single, corr}, events)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// single-event rule matches the file.read event (1); correlation matches s1 (1).
	if len(findings) != 2 {
		t.Fatalf("want 2 findings across both rules, got %d", len(findings))
	}
}
