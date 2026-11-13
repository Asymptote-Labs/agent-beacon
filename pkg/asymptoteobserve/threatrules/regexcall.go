package threatrules

import (
	"regexp"
	"sync"

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
		p := compiledPattern(pattern)
		if p == nil {
			return call, nil
		}
		return &matchesCall{id: call.ID(), function: call.Function(), overload: call.OverloadID(), args: call.Args(), re: p.re, pf: p.pf}, nil
	},
}

// pattern is a compiled pattern and its prefilter, nil when the pattern implies no literal.
type pattern struct {
	re *regexp.Regexp
	pf *prefilter
}

// patterns holds every pattern compiled so far, by its text. A pack's expressions hold some
// 1,700 matches() calls after its predicates are expanded, on a few hundred distinct patterns,
// and an expression is planned twice, with and without cost tracking: compiled where it is
// written, each pattern was compiled, and its prefilter derived, many times over. A compiled
// pattern is immutable and safe to share.
var patterns struct {
	mu     sync.Mutex
	byText map[string]*pattern // nil for a pattern that does not compile
}

// maxSharedPatterns bounds patterns; past it a pattern is compiled where it is written.
const maxSharedPatterns = 8192

// compiledPattern returns the compiled form of a pattern, or nil when it does not compile.
func compiledPattern(text string) *pattern {
	patterns.mu.Lock()
	p, ok := patterns.byText[text]
	patterns.mu.Unlock()
	if ok {
		return p
	}
	if re, err := regexp.Compile(text); err == nil {
		if pf, err := compilePrefilter(text); err == nil {
			if pf.op == pfTrue {
				pf = nil
			}
			p = &pattern{re: re, pf: pf}
		}
	}
	patterns.mu.Lock()
	defer patterns.mu.Unlock()
	if shared, ok := patterns.byText[text]; ok {
		return shared
	}
	if patterns.byText == nil {
		patterns.byText = map[string]*pattern{}
	}
	if len(patterns.byText) < maxSharedPatterns {
		patterns.byText[text] = p
	}
	return p
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
	// A string's Value is the string in an interface, which is an allocation for every call.
	s, ok := "", false
	if str, isString := vals[0].(types.String); isString {
		s, ok = string(str), true
	} else {
		s, ok = vals[0].Value().(string)
	}
	if !ok {
		return types.NoSuchOverloadErr()
	}
	act := eventActivationOf(frame)
	if act == nil {
		if m.pf != nil && !m.pf.holds(canon(s)) {
			return types.False
		}
		return types.Bool(m.re.MatchString(s))
	}
	// The text's atoms are read once for the event, and so is each pattern that gets past its
	// prefilter: every rule that asks the same of the same text shares the answer.
	t := act.atomsOf(s)
	if m.pf != nil && !m.pf.holdsIn(t) {
		return types.False
	}
	return types.Bool(t.matches(m.re))
}

// eventActivationOf returns the event activation a is or wraps, or nil.
func eventActivationOf(a interpreter.Activation) *eventActivation {
	// The bound only guards against an activation chain that never ends; it is far deeper than
	// any nesting of comprehensions a rule can write.
	for range 64 {
		switch x := a.(type) {
		case nil:
			return nil
		case *eventActivation:
			return x
		case *interpreter.ExecutionFrame:
			a = x.Activation
		default:
			a = a.Parent()
		}
	}
	return nil
}
