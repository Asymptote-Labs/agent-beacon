package threatrules

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// matcher is one compiled match expression and the event actions it can be true for.
type matcher struct {
	prog cel.Program
	// ast is the checked expression prog was built from.
	ast     *cel.Ast
	actions map[string]bool // nil: the expression does not limit the action
	// derived is set when the expression selects an engine-derived field.
	derived bool
	// untracked, when set, is the expression without cost tracking, for events within a tier of
	// sizes where its cost cannot reach the limit (matcher.go).
	untracked *untracked
}

func compileMatcher(lib *Library, expr string) (matcher, error) {
	prog, ast, err := lib.compileMatch(expr)
	if err != nil {
		return matcher{}, err
	}
	env, err := lib.environment()
	if err != nil {
		return matcher{}, err
	}
	u, err := newUntracked(env, ast, evalCostLimit)
	if err != nil {
		return matcher{}, fmt.Errorf("build CEL program: %w", err)
	}
	return matcher{prog: prog, ast: ast, actions: eventActions(ast.NativeRep().Expr()), derived: selectsDerivedField(ast), untracked: u}, nil
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

// program returns the program to evaluate on the activation's event: untracked where the
// event's size class lets it, tracked otherwise.
func (m matcher) program(act *eventActivation) cel.Program {
	if m.untracked != nil {
		if c, ok := act.class(); ok && m.untracked.fits(c) {
			return m.untracked.prog
		}
	}
	return m.prog
}

// Tracking an evaluation's cost wraps every step of it and was, once the rest of the fast path was
// in place, most of the time a rule took. The cost limit (MaxEvalCost) only has to stop an
// evaluation that would exceed it, and cel-go can bound an expression's cost before it runs: its
// static estimate, given the largest size each string, bytes, list or map it reads can have, is
// an upper bound of the cost the tracker would count. So an expression is evaluated untracked on
// an event when that bound, for sizes the event is within, fits under the limit: tracked, it
// could not have reached the limit, and its result and errors are the same either way.
//
// The bound is only as good as the sizes it is given, so it is used only where the size of every
// value the expression can see is known:
//
//   - every operation is one whose cost cel-go estimates from its inputs' sizes and whose result is
//     no larger than its inputs (untrackedOverloads); concatenation, replace, format, join, split
//     and anything else that can build a larger string or list keeps the tracker;
//   - every comprehension accumulates a bool or a number, so none builds a list or a map;
//   - a literal has its own size, a value read from the event the size its class allows
//     (eventSizes), and anything else the larger of the two (classSizes).
//
// The sizes are a class: a tier for each of the four measures of eventSizes, the smallest that
// holds the event. The tiers of the two list measures are close together, because a rule's
// cost grows with their product (and with the square of the number of commands, for a rule
// that asks of each command something about the others), and most command lines run a
// handful of commands of a handful of words. An expression's bound for a class is worked out
// the first time an event of that class is evaluated, so a process pays for the classes its
// events are in and not for all of them; an event beyond every tier is evaluated tracked.
// TestUntrackedCostIsBounded checks the bound against the tracked cost on the corpus, and the
// scan equivalence tests (fastpath_test.go) compare results and errors with tracked evaluation,
// down to cost limits low enough that rules fail.
var (
	longTiers   = [...]int{1 << 10, 4 << 10, 16 << 10, 64 << 10}
	wordTiers   = [...]int{64, 128, 256, 1 << 10, 4 << 10, 64 << 10}
	topTiers    = [...]int{2, 4, 8, 16, 32, 64, 256, 1024}
	nestedTiers = [...]int{4, 8, 16, 32, 64, 256, 1024}
)

// sizeClass is one tier of each measure, as one index; noSizeClass is an event beyond a tier.
type sizeClass int

const (
	sizeClasses           = len(longTiers) * len(wordTiers) * len(topTiers) * len(nestedTiers)
	noSizeClass sizeClass = -1
)

// classTiers is a size class as the tier of each measure: long, word, top, nested.
type classTiers [4]uint8

var measureTiers = [len(classTiers{})][]int{longTiers[:], wordTiers[:], topTiers[:], nestedTiers[:]}

// widerTiers gives, for each tier of each measure, the tier a class is widened to before its
// cost is estimated (untracked.resolve): strings in a list up to 256 bytes, 4 KiB or 64 KiB,
// lists up to 16, 64 or 1024 entries.
var widerTiers = [len(classTiers{})][]uint8{
	{0, 1, 2, 3},
	{2, 2, 2, 4, 4, 5},
	{3, 3, 3, 3, 5, 5, 7, 7},
	{2, 2, 2, 4, 4, 6, 6},
}

// classOf returns the smallest class that holds sizes.
func classOf(z eventSizes) sizeClass {
	var t classTiers
	for m, n := range [...]int{z.long, z.word, z.top, z.nested} {
		tier := -1
		for i, size := range measureTiers[m] {
			if n <= size {
				tier = i
				break
			}
		}
		if tier < 0 {
			return noSizeClass
		}
		t[m] = uint8(tier)
	}
	return t.class()
}

func (t classTiers) class() sizeClass {
	c := 0
	for m, tier := range t {
		c = c*len(measureTiers[m]) + int(tier)
	}
	return sizeClass(c)
}

func (c sizeClass) tiers() classTiers {
	var t classTiers
	i := int(c)
	for m := len(t) - 1; m >= 0; m-- {
		t[m], i = uint8(i%len(measureTiers[m])), i/len(measureTiers[m])
	}
	return t
}

// sizes returns the largest sizes of the class.
func (t classTiers) sizes() (long, word, top, nested uint64) {
	return uint64(longTiers[t[0]]), uint64(wordTiers[t[1]]), uint64(topTiers[t[2]]), uint64(nestedTiers[t[3]])
}

func (c sizeClass) sizes() (long, word, top, nested uint64) { return c.tiers().sizes() }

// within reports whether every event of the class is an event of o: no tier of it is above o's.
func (t classTiers) within(o classTiers) bool {
	for m := range t {
		if t[m] > o[m] {
			return false
		}
	}
	return true
}

// widened returns the class with each tier raised to its wider tier.
func (t classTiers) widened() classTiers {
	for m := range t {
		t[m] = widerTiers[m][t[m]]
	}
	return t
}

// untracked is an expression's program without cost tracking, and what bounds its cost.
type untracked struct {
	prog  cel.Program
	env   *cel.Env
	ast   *cel.Ast
	lit   literals
	limit uint64
	// cost holds, for each size class, one more than bound's result for it, and 0 until that
	// has been worked out; learned is what the estimates behind those results have shown.
	cost    [sizeClasses]atomic.Uint32
	learned atomic.Pointer[learned]
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

// newUntracked returns the untracked program for a checked expression, or nil when its shape
// keeps the tracker.
func newUntracked(env *cel.Env, ast *cel.Ast, limit uint64) (*untracked, error) {
	lit, ok := untrackedShape(ast)
	if !ok || limit > math.MaxUint32-2 {
		// The second never holds of MaxEvalCost; a bound is stored in 32 bits.
		return nil, nil
	}
	prog, err := env.Program(ast, cel.OptimizeRegex(prefilteredMatches))
	if err != nil {
		return nil, err
	}
	return &untracked{prog: prog, env: env, ast: ast, lit: lit, limit: limit}, nil
}

// bound returns an upper bound of the expression's cost on an event in size class c, the most
// the tracker could count, or one more than the limit when there is none within the limit. It
// is worked out the first time an event of the class is evaluated and is safe for concurrent
// use.
func (u *untracked) bound(c sizeClass) uint64 {
	if known := u.cost[c].Load(); known != 0 {
		return uint64(known - 1)
	}
	bound := u.resolve(c.tiers())
	u.cost[c].Store(uint32(bound + 1))
	return bound
}

// fits reports whether an evaluation on an event in size class c cannot reach the cost limit.
func (u *untracked) fits(c sizeClass) bool {
	return u.bound(c) <= u.limit
}

// resolve bounds the cost for the class with tiers t from as few estimates as it can, since an
// estimate walks the whole expression and a log holds events of a hundred classes or more.
// The estimate grows with every size, so a class within one that fits fits, with that one's
// estimate as its bound, and a class that holds one over the limit is over it (learned). A
// class neither answers is first widened to tiers most events are within (widened): for most
// expressions that fits, and answers every class within it at once.
func (u *untracked) resolve(t classTiers) uint64 {
	known := u.learned.Load()
	if bound, ok := known.implies(t, u.limit); ok {
		return bound
	}
	if wide := t.widened(); wide != t {
		if _, over := known.implies(wide, u.limit); !over {
			bound := u.estimate(wide)
			u.learn(wide, bound)
			if bound <= u.limit {
				return bound
			}
		}
	}
	bound := u.estimate(t)
	u.learn(t, bound)
	return bound
}

// estimate returns cel-go's estimate of the expression's cost on the largest event of the class
// with tiers t, or one more than the limit when it is over the limit.
func (u *untracked) estimate(t classTiers) uint64 {
	long, word, top, nested := t.sizes()
	est, err := u.env.EstimateCost(u.ast, classSizes{long: long, word: word, top: top, nested: nested, lit: u.lit})
	if err != nil || est.Max > u.limit {
		return u.limit + 1
	}
	return est.Max
}

// learn records the estimate for the class with tiers t.
func (u *untracked) learn(t classTiers, bound uint64) {
	for {
		old := u.learned.Load()
		next := &learned{}
		if old != nil {
			*next = *old
		}
		if bound <= u.limit {
			next.fit = nil
			if old != nil {
				for _, f := range old.fit {
					if !f.tiers.within(t) {
						next.fit = append(next.fit, f)
					}
				}
			}
			next.fit = append(next.fit, fitClass{tiers: t, bound: bound})
		} else {
			next.over = nil
			if old != nil {
				for _, o := range old.over {
					if !t.within(o) {
						next.over = append(next.over, o)
					}
				}
			}
			next.over = append(next.over, t)
		}
		if u.learned.CompareAndSwap(old, next) {
			return
		}
	}
}

// learned is what the estimates made so far say of an expression: the classes found to fit
// under the limit, none within another, and those found over it, none holding another. It is
// never changed once stored.
type learned struct {
	fit  []fitClass
	over []classTiers
}

type fitClass struct {
	tiers classTiers
	bound uint64
}

// implies returns the bound the estimates made so far give the class with tiers t: the
// smallest estimate of a class that fits and holds it, or one more than the limit when it
// holds a class that is over; and false when they say nothing of it.
func (l *learned) implies(t classTiers, limit uint64) (uint64, bool) {
	if l == nil {
		return 0, false
	}
	bound, ok := uint64(0), false
	for _, f := range l.fit {
		if t.within(f.tiers) && (!ok || f.bound < bound) {
			bound, ok = f.bound, true
		}
	}
	if ok {
		return bound, true
	}
	for _, o := range l.over {
		if o.within(t) {
			return limit + 1, true
		}
	}
	return 0, false
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

// classSizes tells cel-go's estimator how large the values of an event in a size class can be,
// leaving call costs to cel-go's own model. cel-go asks it for the size of every node that is
// not a literal, with the path the node's value is read by where it knows one: the variable,
// the fields selected from it, and a marker (@items, @keys, @values, @indices) for each list
// or map whose entries are being read.
//
// A value read from the event is at most as large as its class allows, and which class it is
// in follows from the markers in its path as it does for eventSizes from the lists the value
// is inside. Any other node (the variable of a loop over a list the expression writes, the
// result of a conditional) holds a value of the event or one of the expression's literals, so
// it is given the largest size of either.
type classSizes struct {
	long, word, top, nested uint64
	lit                     literals
}

func (z classSizes) EstimateSize(n checker.AstNode) *checker.SizeEstimate {
	var text bool
	switch n.Type().Kind() {
	case types.StringKind, types.BytesKind:
		text = true
	case types.ListKind, types.MapKind:
	default:
		return nil
	}
	depth, ok := eventPathDepth(n.Path())
	var size uint64
	switch {
	case !ok && text:
		size = max(z.long, z.word, uint64(z.lit.maxString))
	case !ok:
		size = max(z.top, z.nested, uint64(z.lit.maxList))
	case text && depth <= longDepth:
		size = z.long
	case text:
		size = z.word
	case depth == 0:
		size = z.top
	default:
		size = z.nested
	}
	return &checker.SizeEstimate{Min: 0, Max: size}
}

func (classSizes) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}

// eventPathDepth returns how many lists and maps the value at a path is inside, and false for a
// path that does not start at the event.
func eventPathDepth(path []string) (depth int, ok bool) {
	if len(path) == 0 || path[0] != eventVar {
		return 0, false
	}
	for _, segment := range path[1:] {
		if strings.HasPrefix(segment, "@") {
			depth++
		}
	}
	return depth, true
}
