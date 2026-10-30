package threatrules

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Compiling constant matches() patterns once must change nothing a rule can observe: every
// expression in the open corpus and the embedded baseline, on every fixture event and on a
// pessimistic copy whose every string is 64 KiB, gives the same result, the same error, and the
// same CEL cost (the cost limit is a safety contract) as the program cel-go builds by default.
func TestConstantRegexChangesNothing(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", asymptoteobserve.DefaultMaxEventBytes)
	compared := 0
	for _, r := range corpusRules(t) {
		for _, expr := range ruleExpressions(r) {
			ast, iss := env.Compile(expr)
			if iss != nil && iss.Err() != nil {
				t.Fatalf("%s: %v", r.ID, iss.Err())
			}
			plain, err := env.Program(ast, cel.CostLimit(evalCostLimit), cel.EvalOptions(cel.OptTrackCost))
			if err != nil {
				t.Fatalf("%s: %v", r.ID, err)
			}
			optimized, err := env.Program(ast, cel.CostLimit(evalCostLimit), cel.EvalOptions(cel.OptTrackCost), cel.OptimizeRegex(constantRegex))
			if err != nil {
				t.Fatalf("%s: %v", r.ID, err)
			}
			check := func(what string, e asymptoteobserve.Event) {
				in := map[string]any{eventVar: e}
				wantVal, wantDet, wantErr := plain.Eval(in)
				gotVal, gotDet, gotErr := optimized.Eval(in)
				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("%s %s: error %v, default %v", r.ID, what, gotErr, wantErr)
				}
				if wantErr == nil && !reflect.DeepEqual(gotVal.Value(), wantVal.Value()) {
					t.Fatalf("%s %s: %v, default %v", r.ID, what, gotVal, wantVal)
				}
				if *gotDet.ActualCost() != *wantDet.ActualCost() {
					t.Fatalf("%s %s: cost %d, default %d", r.ID, what, *gotDet.ActualCost(), *wantDet.ActualCost())
				}
				compared++
			}
			for _, f := range r.Tests {
				for _, ev := range f.EventList() {
					ev = withDerivedFields(ev)
					check(f.Name, ev)
					fillStrings(reflect.ValueOf(&ev).Elem(), big, 0)
					check(f.Name+" (64 KiB strings)", ev)
				}
			}
		}
	}
	if compared < 1000 {
		t.Fatalf("compared only %d evaluations; the corpus did not load", compared)
	}
	t.Logf("%d evaluations agree", compared)
}

// BenchmarkScanCorpus scans every fixture event of the open corpus and the embedded baseline
// with every rule, as `beacon scan` does.
func BenchmarkScanCorpus(b *testing.B) {
	var compiled []*CompiledRule
	var events []asymptoteobserve.Event
	for _, r := range corpusRules(b) {
		c, err := Compile(r)
		if err != nil {
			b.Fatalf("%s: %v", r.ID, err)
		}
		compiled = append(compiled, c)
		for _, f := range r.Tests {
			events = append(events, f.EventList()...)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ScanEvents(compiled, events); err != nil {
			b.Fatal(err)
		}
	}
}
