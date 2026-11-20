package threatrules

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// These negative-control tests assert that the conformance machinery FAILS when it
// should — so a green pack-conformance run is meaningful. They use synthetic in-memory
// rules and never touch the real pack.

func TestCheckRuleRejectsBogusField(t *testing.T) {
	r := validSingleEventRule()
	r.Match = `e.totally.not.a.field == 1`
	if _, err := CheckRule(r); err == nil {
		t.Fatalf("expected CheckRule to reject a rule referencing an unknown field")
	}
}

func TestCheckRuleEnforcesMaturityGate(t *testing.T) {
	r := validSingleEventRule()
	r.Status = StatusStable
	// Stable but only a match fixture (no no_match) — maturity gate must fail.
	r.Tests = []Fixture{matchFixture()}
	if _, err := CheckRule(r); err == nil {
		t.Fatalf("expected CheckRule to fail the maturity gate for a stable rule lacking a no_match fixture")
	}
}

func TestCheckRuleDetectsFixtureMismatch(t *testing.T) {
	r := validSingleEventRule()
	// Fixture claims match, but the event cannot satisfy the rule's expression.
	r.Tests = []Fixture{
		{Name: "wrong", Verdict: VerdictMatch, Events: []FixtureEvent{
			{Event: asymptoteobserve.Event{Event: asymptoteobserve.EventInfo{Action: "tool.invoked"}}},
		}},
	}
	results, err := CheckRule(r)
	if err != nil {
		t.Fatalf("rule itself is valid, expected no rule-level error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].OK() {
		t.Fatalf("expected fixture to fail (declared match but event does not satisfy rule)")
	}
}

// A rule that cannot be evaluated on one event of a match fixture fails the fixture although
// another event matches: `beacon scan` reports the rule as failing on that event, and lint
// must not pass what a scan reports.
func TestCheckRuleFailsAFixtureTheRuleCannotEvaluateOnEveryEvent(t *testing.T) {
	r := validSingleEventRule()
	r.Spec = "threat-rules/v1.2"
	r.Match = `e.event.action == "command.executed" && (e.command.command == "match me" || e.command.parsed_commands[3].name == "x")`
	r.Tests = []Fixture{
		{Name: "errors_then_matches", Verdict: VerdictMatch, Events: []FixtureEvent{
			{Event: asymptoteobserve.Event{Event: asymptoteobserve.EventInfo{Action: "command.executed"}, Command: &asymptoteobserve.CommandInfo{Command: "ls"}}},
			{Event: asymptoteobserve.Event{Event: asymptoteobserve.EventInfo{Action: "command.executed"}, Command: &asymptoteobserve.CommandInfo{Command: "match me"}}},
		}},
	}
	results, err := CheckRule(r)
	if err != nil {
		t.Fatalf("rule itself is valid, expected no rule-level error: %v", err)
	}
	if len(results) != 1 || results[0].OK() || results[0].Err == nil || results[0].Actual != VerdictMatch {
		t.Fatalf("CheckRule = %+v; want the fixture to fail with the evaluation error and a match verdict", results)
	}
}
