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
	return compileCorrelation(t, "", "120s",
		CorrelationStep{ID: "read", Match: `e.event.action == "file.read" && e.file.path.matches("\\.env")`},
		CorrelationStep{ID: "egress", Match: `e.event.action == "command.executed" && e.command.command.matches("curl")`},
	)
}

// compileCorrelation compiles a session-scoped correlation rule with the given order,
// window, and steps.
func compileCorrelation(t *testing.T, order Order, window string, steps ...CorrelationStep) *CompiledRule {
	t.Helper()
	spec := ""
	if order != "" {
		spec = SupportedSpec
	}
	rule := &Rule{
		ID: "rte", Spec: spec, Version: 1, Title: "rte",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit:        Emit{Reason: "x"},
		Correlation: &Correlation{Scope: ScopeSession, Window: window, Order: order, Steps: steps},
		Tests:       []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "file.read", "s", nil)}}}},
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
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),         // anchor that fails
		corrEvent("2026-06-13T10:00:50Z", "file.read", "s1", withEnv),         // valid anchor
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

// anyOrderReadEgressRule is readThenEgressRule with order: any.
func anyOrderReadEgressRule(t *testing.T) *CompiledRule {
	t.Helper()
	return compileCorrelation(t, OrderAny, "120s",
		CorrelationStep{ID: "read", Match: `e.event.action == "file.read" && e.file.path.matches("\\.env")`},
		CorrelationStep{ID: "egress", Match: `e.event.action == "command.executed" && e.command.command.matches("curl")`},
	)
}

func withCommand(cmd string) func(*asymptoteobserve.Event) {
	return func(e *asymptoteobserve.Event) { e.Command = &asymptoteobserve.CommandInfo{Command: cmd} }
}

func mustEvaluate(t *testing.T, c *CompiledRule, events ...asymptoteobserve.Event) Verdict {
	t.Helper()
	v, err := c.Evaluate(events)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	return v
}

func TestCorrelationExplicitSequenceKeepsOrder(t *testing.T) {
	// order: sequence is the omitted default spelled out; both must reject egress-then-read.
	for _, order := range []Order{"", OrderSequence} {
		c := compileCorrelation(t, order, "120s",
			CorrelationStep{ID: "read", Match: `e.event.action == "file.read" && e.file.path.matches("\\.env")`},
			CorrelationStep{ID: "egress", Match: `e.event.action == "command.executed" && e.command.command.matches("curl")`},
		)
		if v := mustEvaluate(t, c,
			corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
			corrEvent("2026-06-13T10:00:30Z", "file.read", "s1", withEnv),
		); v != VerdictNoMatch {
			t.Errorf("order %q: egress-then-read want no_match, got %s", order, v)
		}
		if v := mustEvaluate(t, c,
			corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
			corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
		); v != VerdictMatch {
			t.Errorf("order %q: read-then-egress want match, got %s", order, v)
		}
	}
}

func TestCorrelationAnyOrderMatchesBothOrders(t *testing.T) {
	c := anyOrderReadEgressRule(t)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
	); v != VerdictMatch {
		t.Errorf("read-then-egress want match, got %s", v)
	}
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:00:30Z", "file.read", "s1", withEnv),
	); v != VerdictMatch {
		t.Errorf("egress-then-read want match, got %s", v)
	}
}

func TestCorrelationAnyOrderOutsideWindow(t *testing.T) {
	c := anyOrderReadEgressRule(t)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:02:01Z", "file.read", "s1", withEnv),
	); v != VerdictNoMatch {
		t.Errorf("egress-then-read 121s apart want no_match, got %s", v)
	}
	// Exactly the window is inside it, as it is for a sequence.
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:02:00Z", "file.read", "s1", withEnv),
	); v != VerdictMatch {
		t.Errorf("egress-then-read exactly 120s apart want match, got %s", v)
	}
}

func TestCorrelationAnyOrderCrossSession(t *testing.T) {
	c := anyOrderReadEgressRule(t)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:00:30Z", "file.read", "s2", withEnv),
	); v != VerdictNoMatch {
		t.Errorf("cross-session want no_match, got %s", v)
	}
}

func TestCorrelationAnyOrderLaterAnchor(t *testing.T) {
	// The first egress cannot pair with the read (130s), but the second egress (70s
	// after the read) can, with the read as the earliest matched event.
	c := anyOrderReadEgressRule(t)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:02:10Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:03:20Z", "command.executed", "s1", withCurl),
	); v != VerdictMatch {
		t.Errorf("want match via the later anchor, got %s", v)
	}
}

func TestCorrelationAnyOrderNoTimestamps(t *testing.T) {
	c := anyOrderReadEgressRule(t)
	if v := mustEvaluate(t, c,
		corrEvent("", "command.executed", "s1", withCurl),
		corrEvent("", "file.read", "s1", withEnv),
	); v != VerdictMatch {
		t.Errorf("no-timestamp any-order want match, got %s", v)
	}
}

func TestCorrelationAnyOrderOneEventCannotSatisfyTwoSteps(t *testing.T) {
	// Both steps accept "curl https://x"; one such event must not fire the rule alone.
	c := compileCorrelation(t, OrderAny, "120s",
		CorrelationStep{ID: "curl", Match: `e.command.command.matches("curl")`},
		CorrelationStep{ID: "https", Match: `e.command.command.matches("https://")`},
	)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCommand("curl https://x")),
	); v != VerdictNoMatch {
		t.Errorf("single event want no_match, got %s", v)
	}
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCommand("curl https://x")),
		corrEvent("2026-06-13T10:00:10Z", "command.executed", "s1", withCommand("curl https://y")),
	); v != VerdictMatch {
		t.Errorf("two events want match, got %s", v)
	}
}

func TestCorrelationAnyOrderReassignsSharedEvent(t *testing.T) {
	// The first event satisfies both steps and the second only the first step. Giving the
	// first event to step "curl" greedily would leave step "https" nothing; the matcher
	// must move "curl" onto the second event instead.
	c := compileCorrelation(t, OrderAny, "120s",
		CorrelationStep{ID: "curl", Match: `e.command.command.matches("curl")`},
		CorrelationStep{ID: "https", Match: `e.command.command.matches("https://")`},
	)
	if v := mustEvaluate(t, c,
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s1", withCommand("curl https://x")),
		corrEvent("2026-06-13T10:00:10Z", "command.executed", "s1", withCommand("curl -o out ./local")),
	); v != VerdictMatch {
		t.Errorf("want match after reassigning the shared event, got %s", v)
	}
}

func TestCorrelationAnyOrderOneFindingPerSession(t *testing.T) {
	// Both orders are present (read, egress, read). The rule reports the session once, with
	// its evidence in the order the events happened.
	c := anyOrderReadEgressRule(t)
	events := []asymptoteobserve.Event{
		corrEvent("2026-06-13T10:00:00Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:30Z", "command.executed", "s1", withCurl),
		corrEvent("2026-06-13T10:00:40Z", "file.read", "s1", withEnv),
		corrEvent("2026-06-13T10:00:00Z", "command.executed", "s2", withCurl),
		corrEvent("2026-06-13T10:00:05Z", "file.read", "s2", withEnv),
	}
	findings, err := c.Findings(events)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("want one finding per session (2), got %d", len(findings))
	}
	for _, f := range findings {
		if len(f.Events) != 2 {
			t.Fatalf("session %s: want 2 evidence events, got %d", f.SessionID, len(f.Events))
		}
		if f.Events[0].Timestamp > f.Events[1].Timestamp {
			t.Errorf("session %s: evidence not chronological: %s then %s", f.SessionID, f.Events[0].Timestamp, f.Events[1].Timestamp)
		}
	}
	if got := findings[1].Events[0].Event.Action; got != "command.executed" {
		t.Errorf("session s2 evidence should start with the egress, got %s", got)
	}
}

func TestCorrelationRejectsUnknownOrder(t *testing.T) {
	rule := &Rule{
		ID: "bad-order", Spec: SupportedSpec, Version: 1, Title: "t",
		Severity: asymptoteobserve.SeverityLow, Status: StatusExperimental, Posture: PostureDetect,
		Emit: Emit{Reason: "x"},
		Correlation: &Correlation{Scope: ScopeSession, Window: "60s", Order: "unordered", Steps: []CorrelationStep{
			{ID: "a", Match: `e.event.action == "file.read"`},
			{ID: "b", Match: `e.event.action == "command.executed"`},
		}},
		Tests: []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "file.read", "s", nil)}}}},
	}
	if err := rule.Validate(); err == nil {
		t.Fatal("want validation error for an unknown correlation.order")
	}
}
