package threatrules

import (
	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// matcher is one compiled match expression and the event actions it can be true for.
type matcher struct {
	prog    cel.Program
	actions map[string]bool // nil: the expression does not limit the action
}

func compileMatcher(expr string) (matcher, error) {
	prog, ast, err := compileMatch(expr)
	if err != nil {
		return matcher{}, err
	}
	return matcher{prog: prog, actions: eventActions(ast.NativeRep().Expr())}, nil
}

// excludes reports whether the expression is false for every event with this action.
func (m matcher) excludes(action string) bool {
	return m.actions != nil && !m.actions[action]
}

// eventActions returns the event actions a match expression can be true for, or nil when it does
// not limit the action. Most rules open with `e.event.action == "..."`, and evaluating each of them
// on every event of another action was most of the work of a scan.
//
// A set is returned only when, on an event whose action is outside it, the expression is false
// having evaluated nothing but comparisons of e.event.action with string literals, which cannot
// fail and cost a few units; not evaluating it then changes no result and no error. That holds for:
//
//   - a pure comparison: `e.event.action == "x"` either way round, `e.event.action in ["x", "y"]`,
//     and || and && of those (actionConstraint);
//   - an && chain, read in the order CEL evaluates it, which stops at the first false operand:
//     its leading pure comparisons, and the operand after them if that one is limited itself
//     (it is the first to run anything else). Nothing after an operand that is not pure is used:
//     that operand runs first, and its error (a runtime error, or exceeding MaxEvalCost) would be
//     the result;
//   - an || whose every operand is limited: each is false in turn, so the whole is.
func eventActions(e celast.Expr) map[string]bool {
	if set, ok := actionConstraint(e); ok {
		return set
	}
	if e.Kind() != celast.CallKind {
		return nil
	}
	switch e.AsCall().FunctionName() {
	case operators.LogicalOr:
		union := map[string]bool{}
		for _, arg := range e.AsCall().Args() {
			set := eventActions(arg)
			if set == nil {
				return nil
			}
			for a := range set {
				union[a] = true
			}
		}
		return union
	case operators.LogicalAnd:
		var actions map[string]bool
		for _, operand := range conjuncts(e, nil) {
			set, pure := actionConstraint(operand)
			if !pure {
				// Not an && (conjuncts flattened those) and not pure: an || or nothing usable.
				set = eventActions(operand)
			}
			if set == nil {
				break
			}
			if actions == nil {
				actions = set
			} else {
				for a := range actions {
					if !set[a] {
						delete(actions, a)
					}
				}
			}
			if !pure {
				break
			}
		}
		return actions
	}
	return nil
}

// conjuncts appends the operands of e's && chain, in evaluation order, or e itself.
func conjuncts(e celast.Expr, out []celast.Expr) []celast.Expr {
	if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == operators.LogicalAnd {
		for _, arg := range e.AsCall().Args() {
			out = conjuncts(arg, out)
		}
		return out
	}
	return append(out, e)
}

// actionConstraint returns the actions e is true for when e only compares e.event.action with
// string literals.
func actionConstraint(e celast.Expr) (map[string]bool, bool) {
	if e.Kind() != celast.CallKind {
		return nil, false
	}
	call := e.AsCall()
	args := call.Args()
	switch call.FunctionName() {
	case operators.LogicalAnd, operators.LogicalOr:
		var out map[string]bool
		for i, arg := range args {
			set, ok := actionConstraint(arg)
			if !ok {
				return nil, false
			}
			switch {
			case i == 0:
				out = set
			case call.FunctionName() == operators.LogicalOr:
				for a := range set {
					out[a] = true
				}
			default:
				for a := range out {
					if !set[a] {
						delete(out, a)
					}
				}
			}
		}
		return out, out != nil
	case operators.Equals:
		if len(args) == 2 {
			if s, ok := stringLiteral(args[1]); ok && isEventAction(args[0]) {
				return map[string]bool{s: true}, true
			}
			if s, ok := stringLiteral(args[0]); ok && isEventAction(args[1]) {
				return map[string]bool{s: true}, true
			}
		}
	case operators.In:
		if len(args) == 2 && isEventAction(args[0]) && args[1].Kind() == celast.ListKind {
			out := map[string]bool{}
			for _, el := range args[1].AsList().Elements() {
				s, ok := stringLiteral(el)
				if !ok {
					return nil, false
				}
				out[s] = true
			}
			return out, true
		}
	}
	return nil, false
}

// isEventAction reports whether e is the field selection e.event.action.
func isEventAction(e celast.Expr) bool {
	if e.Kind() != celast.SelectKind || e.AsSelect().IsTestOnly() || e.AsSelect().FieldName() != "action" {
		return false
	}
	ev := e.AsSelect().Operand()
	if ev.Kind() != celast.SelectKind || ev.AsSelect().IsTestOnly() || ev.AsSelect().FieldName() != "event" {
		return false
	}
	root := ev.AsSelect().Operand()
	return root.Kind() == celast.IdentKind && root.AsIdent() == eventVar
}

func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}
