package threatrules

import (
	"regexp"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"
)

// prefilteredMatches compiles the constant pattern of a matches() call once, when the program is
// built, and runs it behind the pattern's prefilter. Without the compilation cel-go compiles the
// pattern on every evaluation, for every rule and every event.
//
// A pattern that does not compile is left to the call as written, so a rule with a broken pattern
// still loads and still fails when it is evaluated (TestInvalidRegexErrorsAtEval): moving that
// failure to load time would make rulestore skip the rule instead. Results, errors and CEL costs
// are those of the call cel-go builds (TestFastPathChangesNothing).
var prefilteredMatches = &interpreter.RegexOptimization{
	Function:   interpreter.MatchesRegexOptimization.Function,
	RegexIndex: interpreter.MatchesRegexOptimization.RegexIndex,
	Factory: func(call interpreter.InterpretableCall, pattern string) (interpreter.InterpretableCall, error) {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return call, nil
		}
		pf, err := compilePrefilter(pattern)
		if err != nil {
			return call, nil
		}
		if pf.op == pfTrue {
			pf = nil
		}
		return &matchesCall{id: call.ID(), function: call.Function(), overload: call.OverloadID(), args: call.Args(), re: re, pf: pf}, nil
	},
}

// matchesCall is a matches() call with a constant pattern. It keeps the call's identity (id,
// function, overload, arguments) and evaluates every argument as cel-go's own call does, so the
// cost tracker sees the same steps; only how the result is computed changes.
type matchesCall struct {
	id       int64
	function string
	overload string
	args     []interpreter.InterpretableV2
	re       *regexp.Regexp
	pf       *prefilter // nil when the pattern implies no literal
}

func (m *matchesCall) ID() int64                           { return m.id }
func (m *matchesCall) Function() string                    { return m.function }
func (m *matchesCall) OverloadID() string                  { return m.overload }
func (m *matchesCall) Args() []interpreter.InterpretableV2 { return m.args }

func (m *matchesCall) Eval(a interpreter.Activation) ref.Val {
	return m.Exec(interpreter.AsFrame(a))
}

// Exec is cel-go's strict call (interpreter.NewCall): arguments in order, the first error or the
// merged unknowns returned as they are, and the result labeled with the call's node.
func (m *matchesCall) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	vals := make([]ref.Val, len(m.args))
	var unk *types.Unknown
	for i, arg := range m.args {
		vals[i] = arg.Exec(frame)
		if types.IsError(vals[i]) {
			return vals[i]
		}
		unk, _ = types.MaybeMergeUnknowns(vals[i], unk)
	}
	if unk != nil {
		return unk
	}
	return types.LabelErrNode(m.id, m.match(frame, vals))
}

func (m *matchesCall) match(frame *interpreter.ExecutionFrame, vals []ref.Val) ref.Val {
	if len(vals) != 2 {
		return types.NoSuchOverloadErr()
	}
	s, ok := vals[0].Value().(string)
	if !ok {
		return types.NoSuchOverloadErr()
	}
	if m.pf != nil && !m.pf.holds(canonOf(frame, s)) {
		return types.False
	}
	return types.Bool(m.re.MatchString(s))
}

// canonOf is canon(s), from the event activation's cache when a is or wraps one: every rule that
// matches on a field then shares one canonicalization of it.
func canonOf(a interpreter.Activation, s string) string {
	// The bound only guards against an activation chain that never ends; it is far deeper than
	// any nesting of comprehensions a rule can write.
	for range 64 {
		switch x := a.(type) {
		case nil:
			return canon(s)
		case *eventActivation:
			return x.canon(s)
		case *interpreter.ExecutionFrame:
			a = x.Activation
		default:
			a = a.Parent()
		}
	}
	return canon(s)
}
