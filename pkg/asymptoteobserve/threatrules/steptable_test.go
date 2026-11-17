package threatrules

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// stepRule compiles a correlation rule with an id of its own.
func stepRule(t *testing.T, id string, order Order, window string, steps ...CorrelationStep) *CompiledRule {
	t.Helper()
	rule := &Rule{
		ID: id, Spec: SupportedSpec, Version: 1, Title: id,
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit:        Emit{Reason: "x"},
		Correlation: &Correlation{Scope: ScopeSession, Window: window, Order: order, Steps: steps},
		Tests:       []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "a", "s", nil)}}}},
	}
	c, err := Compile(rule)
	if err != nil {
		t.Fatalf("compile %s: %v", id, err)
	}
	return c
}

// ownFindings is what a rule finds in the events with no step table: a single-event rule's match
// evaluated on every event, a correlation rule's sessions matched one by one with each step
// evaluated on its own where the matcher asks it. An evaluation that fails does not hold; the
// first, in the order the rule asks, is returned beside the findings.
func ownFindings(c *CompiledRule, events []asymptoteobserve.Event) ([]Finding, error) {
	var findings []Finding
	var first error
	held := func(m matcher, e asymptoteobserve.Event) bool {
		ok, err := evalAlone(m, e)
		if err != nil && first == nil {
			first = err
		}
		return ok
	}
	if c.steps == nil {
		for _, e := range events {
			if held(c.match, e) {
				findings = append(findings, c.newFinding([]asymptoteobserve.Event{e}))
			}
		}
		return findings, first
	}
	var order []string
	sessions := map[string][]asymptoteobserve.Event{}
	for _, e := range events {
		sid := sessionOf(&e)
		if _, seen := sessions[sid]; !seen {
			order = append(order, sid)
		}
		sessions[sid] = append(sessions[sid], e)
	}
	for _, sid := range order {
		session := sessions[sid]
		seq, _ := c.matchSession(session, func(step, event int) (bool, error) { return held(c.steps[step], session[event]), nil })
		if seq != nil {
			findings = append(findings, c.newFinding(seq))
		}
	}
	return findings, first
}

// ScanEvents answers the correlation rules from one pass over the events. What it returns, the
// findings with their evidence or the error of the first rule that fails, is what each rule
// finds on its own, every step evaluated where its matcher asks (ownFindings): on random
// sessions, with steps several rules share, a step that reads a derived field, and a step that
// exceeds the cost limit on some events, which fails a scan only when a rule's matcher gets as
// far as asking it.
func TestScanEventsMatchesEachCorrelationRule(t *testing.T) {
	lowerCostLimit(t)
	runaway := `e.event.action == "c" && ` + nestedAll(7)
	curl := `e.event.action == "command.executed" && e.command.parsed_commands.exists(c, c.name == "curl")`
	isA, isB := `e.event.action == "a"`, `e.event.action == "b"`
	rules := []*CompiledRule{
		stepRule(t, "a-then-b", "", "10s", CorrelationStep{ID: "a", Match: isA}, CorrelationStep{ID: "b", Match: isB}),
		stepRule(t, "a-ab-c", "", "20s", CorrelationStep{ID: "a", Match: isA},
			CorrelationStep{ID: "ab", Match: `e.event.action in ["a", "b"]`}, CorrelationStep{ID: "c", Match: `e.event.action == "c"`}),
		stepRule(t, "b-then-curl", "", "30s", CorrelationStep{ID: "b", Match: isB}, CorrelationStep{ID: "curl", Match: curl}),
		stepRule(t, "curl-and-a", OrderAny, "30s", CorrelationStep{ID: "curl", Match: curl}, CorrelationStep{ID: "a", Match: isA}),
		stepRule(t, "b-then-runaway", "", "30s", CorrelationStep{ID: "b", Match: isB}, CorrelationStep{ID: "r", Match: runaway}),
		stepRule(t, "curl-and-runaway", OrderAny, "30s", CorrelationStep{ID: "curl", Match: curl}, CorrelationStep{ID: "r", Match: runaway}),
		stepRule(t, "a-b-runaway", "", "30s", CorrelationStep{ID: "a", Match: isA}, CorrelationStep{ID: "b", Match: isB}, CorrelationStep{ID: "r", Match: runaway}),
	}
	single, err := Compile(&Rule{
		ID: "curl-alone", Spec: SupportedSpec, Version: 1, Title: "curl",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit: Emit{Reason: "x"}, Match: curl,
		Tests: []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "command.executed", "s", withCurl)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	actions := []string{"a", "b", "c", "x", "command.executed"}
	rng := rand.New(rand.NewPCG(7, 7))
	base := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	failed, found, clean := 0, 0, 0
	for iter := 0; iter < 600; iter++ {
		events := make([]asymptoteobserve.Event, rng.IntN(18))
		at := base
		for i := range events {
			at = at.Add(time.Duration(rng.IntN(9)) * time.Second)
			ts := at.Format(time.RFC3339)
			if rng.IntN(6) == 0 {
				ts = ""
			}
			action := actions[rng.IntN(len(actions))]
			var mut func(*asymptoteobserve.Event)
			if action == "command.executed" && rng.IntN(2) == 0 {
				mut = withCurl
			}
			events[i] = corrEvent(ts, action, fmt.Sprintf("s%d", rng.IntN(3)), mut)
		}
		// A different subset and order of rules each time, the single-event rule among them.
		var scanned []*CompiledRule
		for _, k := range rng.Perm(len(rules))[:1+rng.IntN(len(rules))] {
			scanned = append(scanned, rules[k])
		}
		if rng.IntN(2) == 0 {
			scanned = append(scanned, single)
		}

		var all, want []Finding
		var wantErr error
		for _, c := range scanned {
			f, err := ownFindings(c, events)
			all = append(all, f...)
			if err != nil && wantErr == nil {
				wantErr = fmt.Errorf("rule %q: %w", c.rule.ID, err)
			}
		}
		if wantErr == nil {
			want = all
		}
		got, gotErr := ScanEvents(scanned, events)
		if (gotErr == nil) != (wantErr == nil) || gotErr != nil && gotErr.Error() != wantErr.Error() {
			t.Fatalf("iter %d: ScanEvents error %v, each rule's %v", iter, gotErr, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iter %d: ScanEvents found %v, each rule found %v", iter, findingKeys(got), findingKeys(want))
		}
		// Scan goes on past a failure, and finds what each rule finds past its own.
		result := Scan(scanned, events)
		if !reflect.DeepEqual(result.Findings, all) || (len(result.Failures) > 0) != (wantErr != nil) {
			t.Fatalf("iter %d: Scan found %v with %d failures, each rule found %v (failing: %v)", iter, findingKeys(result.Findings), len(result.Failures), findingKeys(all), wantErr)
		}
		switch {
		case gotErr != nil:
			failed++
		case len(got) > 0:
			found++
		default:
			clean++
		}
	}
	// The sessions must exercise all three outcomes for the comparison to mean anything.
	if failed < 20 || found < 20 || clean < 20 {
		t.Fatalf("%d scans failed, %d found something, %d found nothing; want each to be common", failed, found, clean)
	}
}

// A scan evaluates a step that several correlation rules share once per event, derives an
// event's fields once however many rules read them, and leaves a later step of a sequence
// alone until the step before it has held in the session.
func TestScanEventsSharesCorrelationWork(t *testing.T) {
	var rules []*CompiledRule
	for _, r := range corpusRules(t) {
		if r.Correlation == nil {
			continue
		}
		c, err := Compile(r)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		rules = append(rules, c)
	}
	// One session of commands none of which is a first step: every later step stays unasked.
	events := make([]asymptoteobserve.Event, 200)
	for i := range events {
		events[i] = corrEvent("", "command.executed", "s1", func(e *asymptoteobserve.Event) {
			e.Command = &asymptoteobserve.CommandInfo{Command: fmt.Sprintf("go test ./pkg%d/... && git status", i)}
		})
	}
	// A step is one expression with one set of definitions, whichever rules write it.
	firstSteps, steps := map[stepKey]bool{}, 0
	for _, c := range rules {
		for s, step := range c.rule.Correlation.Steps {
			steps++
			if (s == 0 || c.anyOrder) && !c.steps[s].excludes("command.executed") {
				firstSteps[stepKey{c.rule.lib, step.Match}] = true
			}
		}
	}

	derivations := 0
	deriveObserver = func() { derivations++ }
	t.Cleanup(func() { deriveObserver = nil })
	evals := countEvals(t)
	found, err := ScanEvents(rules, events)
	if err != nil || len(found) != 0 {
		t.Fatalf("ScanEvents = %v, %v; want nothing", findingKeys(found), err)
	}
	if derivations > len(events) {
		t.Fatalf("derived %d times over %d events; want once per event at most", derivations, len(events))
	}
	if limit := len(firstSteps) * len(events); *evals > limit {
		t.Fatalf("%d evaluations over %d events; want at most %d, one per event for each of the %d distinct steps a matcher asks first (the rules have %d steps)",
			*evals, len(events), limit, len(firstSteps), steps)
	}
	if len(firstSteps) >= len(rules) {
		t.Fatalf("%d distinct first steps for %d rules; the corpus no longer shares one, so this test shows nothing", len(firstSteps), len(rules))
	}
}
