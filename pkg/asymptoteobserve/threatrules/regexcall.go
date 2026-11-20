package threatrules

import (
	"regexp"
	"sync"
	"sync/atomic"

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

// patternTable holds every pattern compiled so far, by its text, and numbers the atoms of their
// prefilters for the automaton that reads an event's texts (textatoms.go). A pack's expressions
// hold some 1,700 matches() calls after its predicates are expanded, on a few hundred distinct
// patterns, and an expression is planned twice, with and without cost tracking: compiled where
// it is written, each pattern was compiled, and its prefilter derived, many times over. A stored
// pattern is immutable and safe to share, and an atom's number never changes.
//
// An atom is numbered when a pattern that holds it is stored, so the atoms are those of the
// stored patterns and nothing else. Two bounds hold the table: past maxSharedPatterns it keeps
// what it holds, as matchCache does, and a pattern is compiled where it is written; past
// maxAtoms an atom is left unnumbered, since one pattern can hold thousands (an alternation of
// literals). An atom without a number is searched for in the text, as every atom was before the
// automaton, so neither bound changes what matches.
var patternTable struct {
	mu     sync.Mutex
	byText map[string]*pattern // nil for a pattern that does not compile
	ids    map[string]uint32   // a canonical atom -> its number, from 1
	atoms  []string            // atoms[number-1]
	count  atomic.Int32        // len(atoms), read without the lock
	built  atomic.Pointer[automaton]
}

// maxSharedPatterns bounds the pattern table, and maxAtoms the atoms it numbers. A pack has a
// few thousand atoms.
const (
	maxSharedPatterns = 8192
	maxAtoms          = 1 << 15
)

// compiledPattern returns the compiled form of a pattern, or nil when it does not compile.
func compiledPattern(text string) *pattern {
	patternTable.mu.Lock()
	p, ok := patternTable.byText[text]
	patternTable.mu.Unlock()
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
	patternTable.mu.Lock()
	defer patternTable.mu.Unlock()
	if shared, ok := patternTable.byText[text]; ok {
		return shared
	}
	if len(patternTable.byText) >= maxSharedPatterns {
		return p
	}
	if patternTable.byText == nil {
		patternTable.byText, patternTable.ids = map[string]*pattern{}, map[string]uint32{}
	}
	if p != nil && p.pf != nil {
		numberAtoms(p.pf)
	}
	patternTable.byText[text] = p
	return p
}

// numberAtoms gives every atom of a prefilter being stored its number, numbering the atoms the
// table does not have yet while it holds fewer than maxAtoms; an atom past that keeps number 0.
// The table's lock is held.
func numberAtoms(pf *prefilter) {
	if pf.op == pfAtom {
		id, ok := patternTable.ids[pf.atom]
		if !ok && len(patternTable.atoms) < maxAtoms {
			patternTable.atoms = append(patternTable.atoms, pf.atom)
			id = uint32(len(patternTable.atoms))
			patternTable.ids[pf.atom] = id
			patternTable.count.Store(int32(id))
		}
		pf.id = id
	}
	for _, sub := range pf.subs {
		numberAtoms(sub)
	}
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
	// The text's atoms are read once for the event, and so is each pattern that gets past its
	// prefilter: every rule that asks the same of the same text shares the answer. A text with
	// no event behind it (a program evaluated outside the engine) is read on its own.
	var t *textAtoms
	if act := eventActivationOf(frame); act != nil {
		t = act.atomsOf(s)
	} else {
		t = plainText(s)
	}
	if m.pf != nil && !m.pf.holds(t) {
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
