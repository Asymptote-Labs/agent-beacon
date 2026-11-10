package threatrules

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// matcher is one compiled match expression and the event actions it can be true for.
type matcher struct {
	prog    cel.Program
	actions map[string]bool // nil: the expression does not limit the action
	// untracked, when set, is the expression without cost tracking, for events within a tier of
	// sizes where its cost cannot reach the limit (matcher.go).
	untracked *untracked
}

func compileMatcher(expr string) (matcher, error) {
	prog, ast, err := compileMatch(expr)
	if err != nil {
		return matcher{}, err
	}
	env, err := Env()
	if err != nil {
		return matcher{}, err
	}
	u, err := newUntracked(env, ast, evalCostLimit)
	if err != nil {
		return matcher{}, fmt.Errorf("build CEL program: %w", err)
	}
	return matcher{prog: prog, actions: eventActions(ast.NativeRep().Expr()), untracked: u}, nil
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

// program returns the program to evaluate on the activation's event: untracked where its tier
// lets it, tracked otherwise.
func (m matcher) program(act *eventActivation) cel.Program {
	if m.untracked != nil {
		if s, l, ok := act.tier(); ok && m.untracked.fits[s][l] {
			return m.untracked.prog
		}
	}
	return m.prog
}

// Tracking an evaluation's cost wraps every step of it and was, once the rest of the fast path was
// in place, most of the time a rule took. The cost limit (MaxEvalCost) only has to stop an
// evaluation that would exceed it, and cel-go can bound an expression's cost before it runs: its
// static estimate, given the largest size any string, bytes, list or map it reads can have, is an
// upper bound of the cost the tracker would count. So an expression is evaluated untracked on an
// event when that bound, for sizes the event is within, fits under the limit: tracked, it could
// not have reached the limit, and its result and errors are the same either way.
//
// The bound is only as good as the sizes it is given, so it is used only where every value the
// expression can see is as large as the event's own values or its literals at most:
//
//   - every operation is one whose cost cel-go estimates from its inputs' sizes and whose result is
//     no larger than its inputs (untrackedOverloads); concatenation, replace, format, join, split
//     and anything else that can build a larger string or list keeps the tracker;
//   - every comprehension accumulates a bool or a number, so none builds a list or a map;
//   - the sizes are a tier at least as large as the event's largest string and list, and as its
//     largest literal.
//
// The estimate is worked out for a few tiers of sizes when the expression is compiled, and an event
// takes the smallest tier that holds it; an event beyond every tier is evaluated tracked.
// TestUntrackedCostIsBounded checks the bound against the tracked cost on the corpus, and the
// scan equivalence tests (fastpath_test.go) compare results and errors with tracked evaluation,
// down to cost limits low enough that rules fail.
var (
	stringTiers = [...]int{1 << 10, 4 << 10, 16 << 10, 64 << 10}
	listTiers   = [...]int{16, 64, 256, 1024}
)

// untracked is an expression's program without cost tracking, and for each tier of sizes whether
// its estimated cost there fits under the limit.
type untracked struct {
	prog cel.Program
	max  [len(stringTiers)][len(listTiers)]uint64 // estimated cost; 0 where a tier is not usable
	fits [len(stringTiers)][len(listTiers)]bool
}

// untrackedOverloads are the operations an expression evaluated untracked may use: cel-go
// estimates the cost of each from the sizes of its inputs (checker/cost.go, as the tracker counts
// it in interpreter/runtimecost.go), and none returns a string, bytes, list or map larger than an
// input. Anything not listed keeps the tracker.
var untrackedOverloads = map[string]bool{
	"logical_and": true, "logical_or": true, "logical_not": true, "not_strictly_false": true,
	"conditional": true,
	"equals":      true, "not_equals": true,
	"less_int64": true, "less_uint64": true, "less_double": true, "less_string": true,
	"less_equals_int64": true, "less_equals_uint64": true, "less_equals_double": true, "less_equals_string": true,
	"greater_int64": true, "greater_uint64": true, "greater_double": true, "greater_string": true,
	"greater_equals_int64": true, "greater_equals_uint64": true, "greater_equals_double": true, "greater_equals_string": true,
	"add_int64": true, "subtract_int64": true, "add_double": true, "subtract_double": true,
	"in_list": true, "in_map": true, "index_list": true, "index_map": true,
	"size_string": true, "size_list": true, "size_map": true, "size_bytes": true,
	"string_size": true, "list_size": true, "map_size": true, "bytes_size": true,
	"matches": true, "matches_string": true, "contains_string": true,
	"starts_with_string": true, "ends_with_string": true,
}

// newUntracked returns the untracked program for a checked expression, or nil when no tier lets it
// run untracked.
func newUntracked(env *cel.Env, ast *cel.Ast, limit uint64) (*untracked, error) {
	lit, ok := untrackedShape(ast)
	if !ok {
		return nil, nil
	}
	u := &untracked{}
	usable := false
	for s, maxString := range stringTiers {
		for l, maxList := range listTiers {
			if lit.maxString > maxString || lit.maxList > maxList {
				continue
			}
			est, err := env.EstimateCost(ast, tierSizes{maxString: uint64(maxString), maxList: uint64(maxList)})
			if err != nil || est.Max > limit {
				continue
			}
			u.max[s][l], u.fits[s][l], usable = est.Max, true, true
		}
	}
	if !usable {
		return nil, nil
	}
	// An event within a tier is within every larger one: where its own tier does not fit (its
	// literals are larger than the tier), the smallest larger tier that fits bounds it.
	for s := len(stringTiers) - 1; s >= 0; s-- {
		for l := len(listTiers) - 1; l >= 0; l-- {
			if u.fits[s][l] {
				continue
			}
			for _, next := range [][2]int{{s + 1, l}, {s, l + 1}} {
				if next[0] < len(stringTiers) && next[1] < len(listTiers) && u.fits[next[0]][next[1]] &&
					(!u.fits[s][l] || u.max[next[0]][next[1]] < u.max[s][l]) {
					u.max[s][l], u.fits[s][l] = u.max[next[0]][next[1]], true
				}
			}
		}
	}
	prog, err := env.Program(ast, cel.OptimizeRegex(prefilteredMatches))
	if err != nil {
		return nil, err
	}
	u.prog = prog
	return u, nil
}

// literals are the largest string and the longest list or map an expression writes itself.
type literals struct{ maxString, maxList int }

// untrackedShape reports whether every operation of the expression is in untrackedOverloads and
// every comprehension accumulates a bool or a number, and returns the sizes of its literals.
func untrackedShape(ast *cel.Ast) (literals, bool) {
	native := ast.NativeRep()
	for _, ref := range native.ReferenceMap() {
		for _, id := range ref.OverloadIDs {
			if !untrackedOverloads[id] {
				return literals{}, false
			}
		}
	}
	var lit literals
	ok := true
	celast.PreOrderVisit(native.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		switch e.Kind() {
		case celast.CallKind:
			// A call the checker left without an overload has nothing to check.
			if ref := native.ReferenceMap()[e.ID()]; ref == nil || len(ref.OverloadIDs) == 0 {
				ok = false
			}
		case celast.ComprehensionKind:
			switch native.GetType(e.AsComprehension().AccuInit().ID()).Kind() {
			case types.BoolKind, types.IntKind, types.UintKind, types.DoubleKind:
			default:
				ok = false
			}
		case celast.LiteralKind:
			switch v := e.AsLiteral().Value().(type) {
			case string:
				lit.maxString = max(lit.maxString, len(v))
			case []byte:
				lit.maxString = max(lit.maxString, len(v))
			}
		case celast.ListKind:
			lit.maxList = max(lit.maxList, len(e.AsList().Elements()))
		case celast.MapKind:
			lit.maxList = max(lit.maxList, len(e.AsMap().Entries()))
		case celast.StructKind:
			// A struct literal is built by the type provider; keep the tracker.
			ok = false
		}
	}))
	return lit, ok
}

// tierSizes tells cel-go's estimator that every string and bytes value is at most maxString long
// and every list and map at most maxList, leaving call costs to cel-go's own model.
type tierSizes struct{ maxString, maxList uint64 }

func (t tierSizes) EstimateSize(n checker.AstNode) *checker.SizeEstimate {
	switch n.Type().Kind() {
	case types.StringKind, types.BytesKind:
		return &checker.SizeEstimate{Min: 0, Max: t.maxString}
	case types.ListKind, types.MapKind:
		return &checker.SizeEstimate{Min: 0, Max: t.maxList}
	}
	return nil
}

func (tierSizes) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}
