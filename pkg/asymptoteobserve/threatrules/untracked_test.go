package threatrules

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// TestUntrackedCostIsBounded evaluates every expression of the corpus both ways wherever the
// untracked program would be chosen: on its rule's fixture events as written, derived, sanitized,
// in fold-case variants and with every string 64 KiB, and on random events with strings and lists
// on either side of the size tiers. Untracked, it must give the tracked program's result and error,
// and the tracked cost must stay within the estimate the choice rested on, which is within the cost
// limit; so an evaluation the limit would stop is never untracked. It runs at the real limit and
// at limits low enough that rules fail.
func TestUntrackedCostIsBounded(t *testing.T) {
	t.Cleanup(func() { evalCostLimit = MaxEvalCost })
	rules := corpusRules(t)
	random := randomEvents(t, rand.New(rand.NewPCG(9, 10)), fixtureEvents(t, rules), 400)
	big := strings.Repeat("a", asymptoteobserve.DefaultMaxEventBytes)
	for _, limit := range []uint64{MaxEvalCost, 10000, 1000, 100} {
		evalCostLimit = limit
		var untracked, tracked, eligible, expressions atomic.Int64
		t.Run(fmt.Sprint("limit ", limit), func(t *testing.T) {
			for _, r := range rules {
				t.Run(r.ID, func(t *testing.T) {
					t.Parallel()
					var events []asymptoteobserve.Event
					for _, e := range fixtureEvents(t, []*Rule{r}) {
						events = append(events, e, asymptoteobserve.SanitizeEvent(copyEvent(t, e), 64*1024))
						v := copyEvent(t, e)
						mapStrings(reflect.ValueOf(&v).Elem(), func(s string) string { return foldVariants(s)[1] })
						events = append(events, v)
						v = copyEvent(t, e)
						fillStrings(reflect.ValueOf(&v).Elem(), big, 0)
						events = append(events, v)
					}
					events = append(events, random...)
					for _, expr := range ruleExpressions(r) {
						m, err := compileMatcher(expr)
						if err != nil {
							t.Fatal(err)
						}
						expressions.Add(1)
						if m.untracked == nil {
							continue
						}
						eligible.Add(1)
						for i, e := range events {
							for _, act := range []*eventActivation{newEventActivation(e), newEventActivation(withDerivedFields(e))} {
								s, l, ok := act.tier()
								if !ok || !m.untracked.fits[s][l] {
									tracked.Add(1)
									continue
								}
								untracked.Add(1)
								checkUntracked(t, m, act, s, l, fmt.Sprintf("event %d", i), expr)
							}
						}
					}
				})
			}
		})
		t.Logf("limit %d: %d of %d expressions can run untracked; %d evaluations untracked, %d tracked",
			limit, eligible.Load(), expressions.Load(), untracked.Load(), tracked.Load())
		if limit == MaxEvalCost && (eligible.Load()*2 < expressions.Load() || untracked.Load() == 0) {
			t.Fatalf("too few untracked evaluations to mean anything")
		}
	}
}

func checkUntracked(t *testing.T, m matcher, act *eventActivation, s, l int, what, expr string) {
	t.Helper()
	gotVal, _, gotErr := m.untracked.prog.Eval(act)
	wantVal, det, wantErr := m.prog.Eval(act)
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
		t.Fatalf("%s: untracked error %v, tracked %v\n%s", what, gotErr, wantErr, expr)
	}
	if wantErr == nil && !reflect.DeepEqual(gotVal.Value(), wantVal.Value()) {
		t.Fatalf("%s: untracked %v, tracked %v\n%s", what, gotVal, wantVal, expr)
	}
	bound := m.untracked.max[s][l]
	if cost := *det.ActualCost(); cost > bound || bound > evalCostLimit {
		t.Fatalf("%s: tracked cost %d, estimate %d, limit %d (tier %d/%d)\n%s", what, cost, bound, evalCostLimit, s, l, expr)
	}
}

// TestUntrackedShape pins which operations keep the tracker: anything that can build a value larger
// than its inputs, a comprehension that builds a list or map, and a struct literal.
func TestUntrackedShape(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		expr string
		ok   bool
	}{
		{`e.event.action == "a" && e.command.command.matches("(?i)curl")`, true},
		{`e.command.parsed_commands.exists(c, c.argv.exists(a, a.startsWith("-")))`, true},
		{`e.command.parsed_commands.exists_one(c, c.name == "rm")`, true},
		{`size(e.command.parsed_commands) > 2 && e.command.parsed_commands[0].name in ["a", "b"]`, true},
		{`(e.command.command + e.file.path).matches("x")`, false},
		{`e.command.parsed_commands.map(c, c.name).exists(n, n == "rm")`, false},
		{`e.command.parsed_commands.filter(c, c.name == "rm").size() > 0`, false},
		{`(e.command.parsed_commands + e.command.parsed_commands).size() > 0`, false},
		{`string(e.command.duration_ms) == "1"`, false},
	} {
		ast, iss := env.Compile(tc.expr)
		if iss != nil && iss.Err() != nil {
			t.Fatalf("%s: %v", tc.expr, iss.Err())
		}
		if _, ok := untrackedShape(ast); ok != tc.ok {
			t.Errorf("%s: untracked %v, want %v", tc.expr, ok, tc.ok)
		}
	}
	// A literal larger than a tier rules that tier out: an event in it is bounded by the next tier.
	ast, _ := env.Compile(`e.command.command == "` + strings.Repeat("x", 2000) + `"`)
	u, err := newUntracked(env, ast, MaxEvalCost)
	if err != nil || u == nil || !u.fits[0][0] || u.max[0][0] != u.max[1][0] {
		t.Fatalf("long literal: %+v, %v", u, err)
	}
	if lit, _ := untrackedShape(ast); lit.maxString != 2000 {
		t.Fatalf("literal size %d", lit.maxString)
	}
}

// TestEventTiers checks the measure an event's tier rests on: the longest string and list a rule can
// read, and nothing a rule cannot (interface{} values such as raw).
func TestEventTiers(t *testing.T) {
	e := asymptoteobserve.Event{
		Command: &asymptoteobserve.CommandInfo{Command: strings.Repeat("x", 5000)},
		Raw:     map[string]interface{}{"huge": strings.Repeat("y", 1<<20)},
	}
	s, l, ok := newEventActivation(e).tier()
	if !ok || s != 2 || l != 0 {
		t.Fatalf("tier %d/%d %v; want 16 KiB strings, 16-element lists", s, l, ok)
	}
	d := withDerivedFields(asymptoteobserve.Event{Command: &asymptoteobserve.CommandInfo{Command: strings.Repeat("a ", 40)}})
	if _, l, ok := newEventActivation(d).tier(); !ok || l != 1 {
		t.Fatalf("derived argv of 40 words: list tier %d %v", l, ok)
	}
	if _, _, ok := newEventActivation(asymptoteobserve.Event{Message: strings.Repeat("z", 70000)}).tier(); ok {
		t.Fatal("a 70,000-byte string fits a tier")
	}
}
