package threatrules

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/traits"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// An event's object keeps the CEL value of each field it has read, lists adapted whole, and
// hands that to every rule after the first. The corpus reads fields and walks lists
// (TestFastPathChangesNothing); these are the other things an expression can do with an object
// or a list of them: compare, index past the end, concatenate, test membership, select
// through dyn, put in a list, a map or a struct literal, test presence. Each is evaluated
// twice on one activation, so the second reads what the first left, and must give the result,
// the error and the cost of cel-go's default program both times.
func TestKeptValuesAnswerAsTheExtension(t *testing.T) {
	exprs := []string{
		`e.command == e.command`,
		`e.command.parsed_commands == e.command.parsed_commands`,
		`dyn(e.command) != dyn(e.file)`,
		`size(e.command.parsed_commands + e.command.parsed_commands) == 2 * size(e.command.parsed_commands)`,
		`e.command.parsed_commands + e.command.parsed_commands == e.command.parsed_commands`,
		`e.command.parsed_commands[0] in e.command.parsed_commands`,
		`e.command.parsed_commands[0] == e.command.parsed_commands[size(e.command.parsed_commands) - 1]`,
		`e.command.parsed_commands[0].name == e.command.parsed_commands.map(c, c.name)[0]`,
		`e.command.parsed_commands[100].name == ""`,
		`e.command.parsed_commands[0].argv[100] == ""`,
		`e.command.parsed_commands[0].argv[0] == e.command.parsed_commands[0].name`,
		`e.command.parsed_commands.map(c, c.argv).exists(words, words.size() > 1 && words[1] in words)`,
		`e.command.parsed_commands.exists(c, c.argv == e.command.parsed_commands[0].argv)`,
		`e.command.parsed_commands.all(c, c.argv_raw.size() == c.argv.size())`,
		`has(e.command.command) || has(e.command.parsed_commands) || has(e.command.exit_code)`,
		`has(e.file.path) == (e.file.path != "")`,
		`dyn(e).command.command == e.command.command`,
		`dyn(e.command).parsed_commands.size() == e.command.parsed_commands.size()`,
		`dyn(e.command).parsed_commands.exists(c, c.name == "curl")`,
		`type(e.command) == type(e.command) && type(e.command.parsed_commands) == list`,
		`[e.command, e.command].exists(c, c.command == e.command.command)`,
		`{"k": e.command}["k"].command == e.command.command`,
		`{"k": e.command.parsed_commands}["k"] == e.command.parsed_commands`,
		`asymptoteobserve.CommandInfo{command: e.command.command}.command == e.command.command`,
		`asymptoteobserve.CommandInfo{command: e.command.command, parsed_commands: e.command.parsed_commands}.parsed_commands == e.command.parsed_commands`,
		`asymptoteobserve.CommandInfo{command: e.command.command} == e.command`,
		`e.command.exit_code == e.command.exit_code && e.command.duration_ms >= 0`,
		`e.timestamp == e.timestamp && e.event.action != ""`,
		`e.gen_ai.tool.call.result_text == e.gen_ai.tool.call.result_text`,
	}
	type progs struct {
		expr      string
		fast, def cel.Program
	}
	var lib *Library
	var ps []progs
	for _, expr := range exprs {
		fast, err := lib.CompileMatch(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		ps = append(ps, progs{expr: expr, fast: fast, def: nativeProgram(t, lib, expr)})
	}
	fixtures := fixtureEvents(t, corpusRules(t))
	if len(fixtures) < 500 {
		t.Fatalf("only %d fixture events; the corpus did not load", len(fixtures))
	}
	for i, fixture := range fixtures {
		e := withDerivedFields(fixture)
		act := newEventActivation(e)
		for _, p := range ps {
			wantVal, wantDet, wantErr := p.def.Eval(map[string]any{eventVar: e})
			for _, read := range []string{"first", "second"} {
				gotVal, gotDet, gotErr := p.fast.Eval(act)
				if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
					t.Fatalf("event %d, %s read: error %v, the extension's %v\n%s", i, read, gotErr, wantErr, p.expr)
				}
				if wantErr == nil && !reflect.DeepEqual(gotVal.Value(), wantVal.Value()) {
					t.Fatalf("event %d, %s read: %v, the extension's %v\n%s", i, read, gotVal, wantVal, p.expr)
				}
				if *gotDet.ActualCost() != *wantDet.ActualCost() {
					t.Fatalf("event %d, %s read: cost %d, the extension's %d\n%s", i, read, *gotDet.ActualCost(), *wantDet.ActualCost(), p.expr)
				}
			}
		}
	}
}

// A list adapted whole holds what the list cel-go builds over the same slice gives, element by
// element. The extension reads a field that is a nil pointer as a zero struct and one that is
// a zero time as the Unix epoch; an element of a list is neither.
func TestListAdaptedWholeHoldsTheSameElements(t *testing.T) {
	a := fastTypes()
	if a == nil {
		t.Fatal("the indexed types are not installed")
	}
	for _, list := range []any{
		[]string{"a", ""},
		[][]string{{"a", "b"}, nil},
		[]asymptoteobserve.ParsedCommand{{Name: "ls", Argv: []string{"ls"}}, {}},
		[]*asymptoteobserve.CommandInfo{nil, {Command: "ls"}},
		[]time.Time{{}, time.Unix(5, 0)},
		[2]int{1, 2},
		[]any{nil, "a", 1, []string{"b"}},
	} {
		whole := a.adapt(reflect.ValueOf(list), true).(traits.Lister)
		want := a.NativeToValue(list).(traits.Lister)
		if whole.Size() != want.Size() {
			t.Fatalf("%T: %v elements, want %v", list, whole.Size(), want.Size())
		}
		for i := range int(want.Size().(types.Int)) {
			got, want := whole.Get(types.Int(i)), want.Get(types.Int(i))
			if types.IsError(want) {
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%T[%d]: %v, want the error %v", list, i, got, want)
				}
				continue
			}
			if got.Type() != want.Type() || got.Equal(want) != types.True {
				t.Errorf("%T[%d]: %v (%v), want %v (%v)", list, i, got, got.Type(), want, want.Type())
			}
		}
	}
}
