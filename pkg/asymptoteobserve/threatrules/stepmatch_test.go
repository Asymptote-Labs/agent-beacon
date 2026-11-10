package threatrules

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// A step that fails does not hide a later step that matches; with none matching, the first failure
// is returned, naming its rule and step. Rules that are not correlations are rejected.
func TestAnyStepErrors(t *testing.T) {
	lowerCostLimit(t)
	compile := func(id string, steps ...CorrelationStep) *CompiledRule {
		c, err := Compile(&Rule{
			ID: id, Spec: SupportedSpec, Version: 1, Title: "x",
			Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
			Emit:        Emit{Reason: "x"},
			Correlation: &Correlation{Scope: ScopeSession, Window: "60s", Steps: steps},
			Tests:       []Fixture{{Name: "x", Verdict: VerdictNoMatch, Events: []FixtureEvent{{Event: corrEvent("", "z", "s", nil)}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	runaway := compile("runaway", CorrelationStep{ID: "slow", Match: nestedAll(7)}, CorrelationStep{ID: "b", Match: `e.event.action == "b"`})
	later := compile("later", CorrelationStep{ID: "a", Match: `e.event.action == "a"`}, CorrelationStep{ID: "c", Match: `e.event.action == "c"`})
	m, err := NewStepMatcher([]*CompiledRule{runaway, later})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := m.AnyStep(PrepareEvent(corrEvent("", "a", "s", nil))); !ok || err != nil {
		t.Fatalf("AnyStep = %v, %v; want the later step's match", ok, err)
	}
	// "y" makes the nested all() run every iteration (it stops early on action "x").
	ok, err := m.AnyStep(PrepareEvent(corrEvent("", "y", "s", nil)))
	if ok || err == nil || !strings.Contains(err.Error(), `rule "runaway" step "slow"`) || !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("AnyStep = %v, %v; want the runaway step's error", ok, err)
	}
	single, err := Compile(validSingleEventRule())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStepMatcher([]*CompiledRule{single}); err == nil {
		t.Fatal("NewStepMatcher accepted a single-event rule")
	}
}

// BenchmarkPreparedSteps judges one event with the single-event rules and asks whether it matches a
// step of a correlation rule, three ways: the single-event rules alone (ScanEvent); those, then
// every distinct step evaluated on its own (ScanEvent+steps, each step binding and deriving the
// event again); and one prepared event for both, stopping at the first step that holds
// (Prepared+AnyStep).
func BenchmarkPreparedSteps(b *testing.B) {
	var single, corr []*CompiledRule
	for _, r := range corpusRules(b) {
		c, err := Compile(r)
		if err != nil {
			b.Fatalf("%s: %v", r.ID, err)
		}
		if c.steps == nil {
			single = append(single, c)
		} else {
			corr = append(corr, c)
		}
	}
	scanner, err := NewEventScanner(single)
	if err != nil {
		b.Fatal(err)
	}
	steps, err := NewStepMatcher(corr)
	if err != nil {
		b.Fatal(err)
	}
	type distinct struct {
		rule *CompiledRule
		step matcher
	}
	var each []distinct
	seen := map[string]bool{}
	for _, c := range corr {
		for i, st := range c.rule.Correlation.Steps {
			if !seen[st.Match] {
				seen[st.Match] = true
				each = append(each, distinct{c, c.steps[i]})
			}
		}
	}
	events := scanBenchEvents(b)
	events = append(events, namedEvent{"file.read_call", decodeBenchEvent(b, `{"timestamp":"2023-11-14T22:13:20.000000000Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","severity":"info","harness":{"name":"claude_code","version":"2.1.306"},"session":{"id":"a0"},"event":{"kind":"agent_runtime","action":"file.read","category":"file","fidelity":"inferred"},"tool":{"name":"Read","path":"/home/dev/project/internal/cache/lru.go"},"file":{"path":"/home/dev/project/internal/cache/lru.go","operation":"read"},"gen_ai":{"conversation":{"id":"a0"},"tool":{"name":"Read","call":{"id":"toolu_7c20e02dae62425abf1f477f","arguments":"{\"file_path\":\"/home/dev/project/internal/cache/lru.go\",\"limit\":200}"}}},"model":"claude-sonnet-4-5-20250929"}`)})
	for _, bc := range events {
		b.Run(bc.name+"/ScanEvent", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := scanner.ScanEvent(bc.event); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(bc.name+"/ScanEvent+steps", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := scanner.ScanEvent(bc.event); err != nil {
					b.Fatal(err)
				}
				for _, d := range each {
					if _, err := d.rule.eval(d.step, bc.event); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(bc.name+"/Prepared+AnyStep", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p := PrepareEvent(bc.event)
				if _, err := scanner.ScanPrepared(p); err != nil {
					b.Fatal(err)
				}
				if _, err := steps.AnyStep(p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
