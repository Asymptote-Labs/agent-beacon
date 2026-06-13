package threatrules

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// corrEvent builds an event for correlation tests.
func corrEvent(ts, action, sessionID string, mut func(*asymptoteobserve.Event)) asymptoteobserve.Event {
	e := asymptoteobserve.Event{
		Timestamp: ts,
		Event:     asymptoteobserve.EventInfo{Action: action},
		Session:   &asymptoteobserve.SessionInfo{ID: sessionID},
	}
	if mut != nil {
		mut(&e)
	}
	return e
}

func readThenEgressRule(t *testing.T) *CompiledRule {
	t.Helper()
	rule := &Rule{
		ID: "rte", Version: 1, Title: "rte",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit: Emit{Reason: "x"},
		Correlation: &Correlation{
			Scope: ScopeSession, Window: "120s",
			Steps: []CorrelationStep{
				{ID: "read", Match: `e.event.action == "file.read" && e.file.path.matches("\\.env")`},
				{ID: "egress", Match: `e.event.action == "command.executed" && e.command.command.matches("curl")`},
			},
		},
		Tests: []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "file.read", "s", nil)}}}},
	}
	c, err := Compile(rule)
	if err != nil {
		t.Fatalf("compile correlation rule: %v", err)
	}
	return c
}

func withEnv(e *asymptoteobserve.Event) { e.File = &asymptoteobserve.FileInfo{Path: ".env"} }
func withCurl(e *asymptoteobserve.Event) {
	e.Command = &asymptoteobserve.CommandInfo{Command: "curl https://x"}
}

func TestCorrelationOrderedInWindow(t *testing.T) {
	c := readThenEgressRule(t)
	v, err := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if v != VerdictMatch {
		t.Fatalf("want match, got %s", v)
	}
}

func TestCorrelationOutOfOrder(t *testing.T) {
	c := readThenEgressRule(t)
	v, _ := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:00:30Z", "file.read", "s1", withEnv),
	})
	if v != VerdictNoMatch {
		t.Fatalf("out-of-order want no_match, got %s", v)
	}
}

func TestCorrelationOutsideWindow(t *testing.T) {
	c := readThenEgressRule(t)
	v, _ := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:05:00Z", "command.executed", "s1", withCurl),
	})
	if v != VerdictNoMatch {
		t.Fatalf("outside-window want no_match, got %s", v)
	}
}

func TestCorrelationCrossSession(t *testing.T) {
	c := readThenEgressRule(t)
	v, _ := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s2", withCurl),
	})
	if v != VerdictNoMatch {
		t.Fatalf("cross-session want no_match, got %s", v)
	}
}

func TestCorrelationUnrelatedEventBetweenSteps(t *testing.T) {
	c := readThenEgressRule(t)
	v, err := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:10Z", "tool.invoked", "s1", nil),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if v != VerdictMatch {
		t.Fatalf("unrelated event between steps should not break match, got %s", v)
	}
}

func TestCorrelationLaterAnchorAfterFailedAlignment(t *testing.T) {
	// An early step-0 match (read@10:00:00) cannot complete in-window: the only egress
	// is 130s later. But a *second* step-0 match (read@10:00:50) forms a valid 80s pair
	// with that same egress. The evaluator must try the later read as a fresh anchor
	// rather than reporting no_match after the first alignment fails.
	c := readThenEgressRule(t)
	v, err := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),       // anchor that fails
		corrEvent("2026-06-13T10:00:50Z", "file.read", "s1", withEnv),       // valid anchor
		corrEvent("2026-06-13T10:02:10Z", "command.executed", "s1", withCurl), // 130s after #1, 80s after #2
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if v != VerdictMatch {
		t.Fatalf("expected match via the later read anchor, got %s", v)
	}
}

func TestCorrelationNoTimestampsStillMatches(t *testing.T) {
	// Without timestamps the window is not enforced, so an in-order sequence matches.
	c := readThenEgressRule(t)
	v, err := c.Evaluate([]asymptoteobserve.Event{
		corrEvent("", "file.read", "s1", withEnv),
		corrEvent("", "command.executed", "s1", withCurl),
	})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if v != VerdictMatch {
		t.Fatalf("no-timestamp in-order want match, got %s", v)
	}
}
