package threatrules

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// nestedAll is a match expression that runs 10^depth comprehension iterations per event:
// at depth 7 it took about six seconds on one event before evaluation was cost-limited.
func nestedAll(depth int) string {
	const list = "[0,1,2,3,4,5,6,7,8,9]"
	expr := `e.event.action != "x"`
	for d := 0; d < depth; d++ {
		expr = fmt.Sprintf("%s.all(v%d, %s)", list, d, expr)
	}
	return expr
}

func countEvals(t *testing.T) *int {
	t.Helper()
	n := 0
	evalObserver = func() { n++ }
	t.Cleanup(func() { evalObserver = nil })
	return &n
}

// lowerCostLimit makes runaway expressions hit the cost limit after 100k units instead of
// MaxEvalCost, so the tests exercise the same error path in milliseconds.
func lowerCostLimit(t *testing.T) {
	t.Helper()
	evalCostLimit = 100_000
	t.Cleanup(func() { evalCostLimit = MaxEvalCost })
}

func TestEvalCostLimitStopsRunawayExpression(t *testing.T) {
	lowerCostLimit(t)
	prog, err := CompileMatch(nestedAll(7))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	start := time.Now()
	matched, err := EvalMatch(prog, asymptoteobserve.Event{})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("EvalMatch = %v, %v; want a cost limit error", matched, err)
	}
	if matched {
		t.Fatal("an expression stopped by the cost limit must not match")
	}
	// The limit is deterministic (cel-go cost units, not time); the bound only guards
	// against the limit being dropped, so it is far above what the limit allows.
	if elapsed > 30*time.Second {
		t.Fatalf("cost-limited evaluation took %s", elapsed)
	}

	// A cheap nested expression stays well under the limit and evaluates normally.
	small, err := CompileMatch(nestedAll(3))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ok, err := EvalMatch(small, asymptoteobserve.Event{}); err != nil || !ok {
		t.Fatalf("depth-3 EvalMatch = %v, %v; want true, nil", ok, err)
	}
}

func TestScanNamesTheRuleThatExceededTheCostLimit(t *testing.T) {
	lowerCostLimit(t)
	rule := &Rule{
		ID: "runaway", Version: 1, Title: "runaway",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit:  Emit{Reason: "x"},
		Match: nestedAll(7),
		Tests: []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "file.read", "s", nil)}}}},
	}
	c, err := Compile(rule)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// CheckRule (run at install by `beacon rules add|pull`) reports it as a fixture error.
	results, err := CheckRule(rule)
	if err != nil || len(results) != 1 || results[0].Err == nil || results[0].OK() {
		t.Fatalf("CheckRule = %+v, %v; want one failing fixture with an error", results, err)
	}
	_, err = ScanEvents([]*CompiledRule{c}, []asymptoteobserve.Event{corrEvent("", "file.read", "s", nil)})
	if err == nil || !strings.Contains(err.Error(), `rule "runaway"`) || !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("ScanEvents error = %v; want it to name the rule and the cost limit", err)
	}
}

// corpusRules loads the open corpus and the embedded baseline.
func corpusRules(t *testing.T) []*Rule {
	t.Helper()
	var all []*Rule
	for _, dir := range []string{rulesDir(t), filepath.Join(repoRoot(t), "pkg", "asymptoteobserve", "rulestore", "baseline")} {
		rules, err := LoadDir(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		all = append(all, rules...)
	}
	return all
}

func ruleExpressions(r *Rule) []string {
	if r.Correlation == nil {
		return []string{r.Match}
	}
	var exprs []string
	for _, s := range r.Correlation.Steps {
		exprs = append(exprs, s.Match)
	}
	return exprs
}

// fillStrings sets every empty string field reachable from v (allocating nil sub-objects)
// to s.
func fillStrings(v reflect.Value, s string, depth int) {
	switch v.Kind() {
	case reflect.Ptr:
		if depth > 6 {
			return
		}
		if v.IsNil() && v.CanSet() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		if !v.IsNil() {
			fillStrings(v.Elem(), s, depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillStrings(v.Field(i), s, depth+1)
			}
		}
	case reflect.String:
		if v.CanSet() && v.Len() == 0 {
			v.SetString(s)
		}
	}
}

// TestCorpusStaysUnderEvalCostLimit measures the cost every shipped expression spends on
// its own fixtures and on a pessimistic copy of each fixture event whose every string field
// is 64 KiB (the writer caps a whole event line at 64 KiB, so no real event gets close), and
// requires headroom under MaxEvalCost. If a new rule trips this, the rule is doing far more
// work per event than anything in the corpus; look at it before raising the limit.
func TestCorpusStaysUnderEvalCostLimit(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", asymptoteobserve.DefaultMaxEventBytes)
	var maxFixture, maxPessimistic uint64
	var maxFixtureID, maxPessimisticID string
	for _, r := range corpusRules(t) {
		for _, expr := range ruleExpressions(r) {
			ast, iss := env.Compile(expr)
			if iss != nil && iss.Err() != nil {
				t.Fatalf("%s: %v", r.ID, iss.Err())
			}
			prog, err := env.Program(ast, cel.EvalOptions(cel.OptTrackCost))
			if err != nil {
				t.Fatalf("%s: %v", r.ID, err)
			}
			cost := func(e asymptoteobserve.Event) uint64 {
				_, det, err := prog.Eval(map[string]any{eventVar: e})
				if err != nil {
					return 0 // a type error on a synthetic value is not a cost measurement
				}
				return *det.ActualCost()
			}
			for _, f := range r.Tests {
				for _, ev := range f.EventList() {
					ev = withDerivedFields(ev)
					if c := cost(ev); c > maxFixture {
						maxFixture, maxFixtureID = c, r.ID
					}
					fillStrings(reflect.ValueOf(&ev).Elem(), big, 0)
					if c := cost(ev); c > maxPessimistic {
						maxPessimistic, maxPessimisticID = c, r.ID
					}
				}
			}
		}
	}
	t.Logf("max cost: fixtures %d (%s), every string 64 KiB %d (%s), limit %d",
		maxFixture, maxFixtureID, maxPessimistic, maxPessimisticID, MaxEvalCost)
	if maxFixture == 0 || maxPessimistic == 0 {
		t.Fatal("measured no cost; the measurement is broken")
	}
	if maxFixture*100 > MaxEvalCost {
		t.Errorf("rule %s spends %d on its fixtures, within 100x of MaxEvalCost %d", maxFixtureID, maxFixture, MaxEvalCost)
	}
	if maxPessimistic*5 > MaxEvalCost {
		t.Errorf("rule %s spends %d on a 64 KiB-string event, within 5x of MaxEvalCost %d", maxPessimisticID, maxPessimistic, MaxEvalCost)
	}
}

// manyAnchors is a session where every event satisfies step 0 of
// secret-read-then-external-mcp and none satisfies step 1: the shape that made ordered
// correlation quadratic.
func manyAnchors(n int) []asymptoteobserve.Event {
	base := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	events := make([]asymptoteobserve.Event, n)
	for i := range events {
		events[i] = corrEvent(base.Add(time.Duration(i)*time.Second).Format(time.RFC3339), "file.read", "s1", withEnv)
	}
	return events
}

func loadCorpusRule(t *testing.T, rel string) *CompiledRule {
	t.Helper()
	rule, err := LoadRule(filepath.Join(rulesDir(t), rel))
	if err != nil {
		t.Fatalf("load %s: %v", rel, err)
	}
	c, err := Compile(rule)
	if err != nil {
		t.Fatalf("compile %s: %v", rel, err)
	}
	return c
}

// TestOrderedCorrelationIsLinearInTheSession counts evaluations rather than timing them: each
// step's expression must run at most once per event. Before, 8,000 anchors re-evaluated every
// later event (about 32 million evaluations, over half a minute).
func TestOrderedCorrelationIsLinearInTheSession(t *testing.T) {
	c := loadCorpusRule(t, "context-exfiltration/secret-read-then-external-mcp.rule.yaml")
	const n = 2000
	events := manyAnchors(n)

	evals := countEvals(t)
	v, err := c.Evaluate(events)
	if err != nil || v != VerdictNoMatch {
		t.Fatalf("Evaluate = %v, %v; want no_match", v, err)
	}
	if limit := n * len(c.steps); *evals > limit {
		t.Fatalf("ordered correlation ran %d evaluations over %d events and %d steps; want at most %d", *evals, n, len(c.steps), limit)
	}

	// The same session with the external MCP call at the end, out of window from every
	// anchor but the last few: still linear, and still finds the in-window alignment.
	end, _ := eventTime(events[n-1])
	events = append(events, corrEvent(end.Add(10*time.Second).Format(time.RFC3339), "mcp.tool_invoked", "s1", func(e *asymptoteobserve.Event) {
		e.MCP = &asymptoteobserve.MCPInfo{Server: "https://api.example.com", Tool: "x"}
	}))
	*evals = 0
	findings, err := c.Findings(events)
	if err != nil || len(findings) != 1 {
		t.Fatalf("Findings = %v, %v; want one finding", findings, err)
	}
	if got := findings[0].Events[0].Timestamp; got == events[0].Timestamp {
		t.Fatalf("finding anchored at the first read (%s), which is outside the window", got)
	}
	if limit := len(events) * len(c.steps); *evals > limit {
		t.Fatalf("ran %d evaluations; want at most %d", *evals, limit)
	}
}

// matchSessionInSequenceReference is the exhaustive matcher the cursors replaced: for every
// anchor it re-scans every later event. It is the oracle for the equivalence test.
func (c *CompiledRule) matchSessionInSequenceReference(events []asymptoteobserve.Event) ([]asymptoteobserve.Event, error) {
	for start := range events {
		matched, err := c.eval(c.steps[0], events[start])
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		startTime, startKnown := eventTime(events[start])
		final := len(c.steps) - 1
		stepIdx := 1
		seq := []asymptoteobserve.Event{events[start]}
		for j := start + 1; j < len(events); j++ {
			matched, err := c.eval(c.steps[stepIdx], events[j])
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
			seq = append(seq, events[j])
			if stepIdx == final {
				t, known := eventTime(events[j])
				if startKnown && known && t.Sub(startTime) > c.window {
					seq = nil
				}
				break
			}
			stepIdx++
		}
		if len(seq) == len(c.steps) {
			return seq, nil
		}
	}
	return nil, nil
}

// TestOrderedCorrelationMatchesExhaustiveSearch compares the cursor matcher with the
// exhaustive one on random sessions: unsorted input, missing timestamps, events that
// satisfy several steps, and windows that cut alignments short.
func TestOrderedCorrelationMatchesExhaustiveSearch(t *testing.T) {
	actions := []string{"a", "b", "c", "x"}
	rules := []*CompiledRule{
		compileCorrelation(t, "", "10s",
			CorrelationStep{ID: "a", Match: `e.event.action == "a"`},
			CorrelationStep{ID: "b", Match: `e.event.action == "b"`},
		),
		compileCorrelation(t, "", "20s",
			CorrelationStep{ID: "a", Match: `e.event.action == "a"`},
			CorrelationStep{ID: "ab", Match: `e.event.action in ["a", "b"]`},
			CorrelationStep{ID: "c", Match: `e.event.action == "c"`},
		),
		compileCorrelation(t, "", "5s",
			CorrelationStep{ID: "a", Match: `e.event.action == "a"`},
			CorrelationStep{ID: "a2", Match: `e.event.action == "a"`},
			CorrelationStep{ID: "b", Match: `e.event.action != "x"`},
		),
	}
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	for iter := 0; iter < 500; iter++ {
		n := rng.Intn(14)
		events := make([]asymptoteobserve.Event, n)
		at := base
		for i := range events {
			at = at.Add(time.Duration(rng.Intn(8)) * time.Second)
			if rng.Intn(6) == 0 {
				at = at.Add(-time.Duration(rng.Intn(15)) * time.Second) // out of order
			}
			ts := at.Format(time.RFC3339)
			if rng.Intn(5) == 0 {
				ts = "" // no timestamp: the window is not enforced against it
			}
			events[i] = corrEvent(ts, actions[rng.Intn(len(actions))], "s", nil)
		}
		for ri, c := range rules {
			got, err := c.matchSessionInSequence(events)
			if err != nil {
				t.Fatal(err)
			}
			want, err := c.matchSessionInSequenceReference(events)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("iter %d rule %d: cursor matcher %v, exhaustive %v, events %v", iter, ri, got, want, events)
			}
		}
	}
}

func TestCostLimitErrorIsNotAMatch(t *testing.T) {
	// Guard the contract callers rely on: the error is returned, never folded into a verdict.
	lowerCostLimit(t)
	c, err := Compile(&Rule{
		ID: "runaway-step", Spec: SupportedSpec, Version: 1, Title: "x",
		Severity: asymptoteobserve.SeverityHigh, Status: StatusExperimental, Posture: PostureDetect,
		Emit: Emit{Reason: "x"},
		Correlation: &Correlation{Scope: ScopeSession, Window: "60s", Steps: []CorrelationStep{
			{ID: "a", Match: `e.event.action == "a"`},
			{ID: "b", Match: nestedAll(7)},
		}},
		Tests: []Fixture{{Name: "x", Verdict: VerdictMatch, Events: []FixtureEvent{{Event: corrEvent("", "a", "s", nil)}}}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v, err := c.Evaluate([]asymptoteobserve.Event{corrEvent("", "a", "s", nil), corrEvent("", "b", "s", nil)})
	if err == nil || v == VerdictMatch || !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("Evaluate = %v, %v; want an error and no match", v, err)
	}
}
