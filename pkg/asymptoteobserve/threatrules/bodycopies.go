package threatrules

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// PredicateCopies returns the names, sorted, of the predicates of rule's pack whose body the
// rule writes out instead of calling: some part of one of its expressions is the body with the
// predicate's parameters replaced, whatever the rule names its own variables. Such a rule
// means what the predicate means today and stops when the predicate changes, which is what a
// predicate exists to prevent.
//
// The rule and the bodies are read as written (asWritten), so a call to a predicate is a call,
// and a rule that carries `x.file_reads.exists(f, credential_file(f.path))` carries the body of
// the predicate defined as exactly that. A body that is an operand of a longer `||` or `&&` in
// the rule counts: the rule says the predicate and something more. A predicate a catalog
// provides has no written body (see CatalogCopies). A rule loaded without definitions has
// nothing to copy.
func PredicateCopies(rule *Rule) []string {
	if rule.lib == nil {
		return nil
	}
	copied := map[string]bool{}
	for _, e := range rule.written() {
		celast.PreOrderVisit(e, celast.NewExprVisitor(func(e celast.Expr) {
			for _, name := range rule.lib.order {
				p := rule.lib.predicates[name]
				if p.written == nil || copied[name] {
					continue
				}
				u := unifier{params: p.params, bound: map[string]string{}, args: map[string]celast.Expr{}}
				if u.unify(p.written, e, true) && len(u.args) == len(p.params) {
					copied[name] = true
				}
			}
		}))
	}
	return slices.Sorted(maps.Keys(copied))
}

// unifier matches a predicate's body against an expression of a rule.
type unifier struct {
	params []string
	// args is what each parameter stands for in the rule, once it has been met.
	args map[string]celast.Expr
	// bound maps a variable the body binds (the f of exists(f, ..)) to the rule's name for it.
	bound map[string]string
}

// unify reports whether e is body with its parameters replaced. With root set, a body that is
// an `||` or an `&&` may be some of the operands of a longer one in e.
func (u *unifier) unify(body, e celast.Expr, root bool) bool {
	if body.Kind() == celast.IdentKind {
		name := body.AsIdent()
		if _, isBound := u.bound[name]; !isBound && slices.Contains(u.params, name) {
			if have, ok := u.args[name]; ok {
				return sameExpr(have, e)
			}
			u.args[name] = e
			return true
		}
	}
	if body.Kind() != e.Kind() {
		return false
	}
	switch body.Kind() {
	case celast.IdentKind:
		if want, ok := u.bound[body.AsIdent()]; ok {
			return e.AsIdent() == want
		}
		return body.AsIdent() == e.AsIdent()
	case celast.LiteralKind:
		return body.AsLiteral().Equal(e.AsLiteral()) == types.True
	case celast.SelectKind:
		b, s := body.AsSelect(), e.AsSelect()
		return b.FieldName() == s.FieldName() && b.IsTestOnly() == s.IsTestOnly() && u.unify(b.Operand(), s.Operand(), false)
	case celast.CallKind:
		b, c := body.AsCall(), e.AsCall()
		if b.FunctionName() != c.FunctionName() || b.IsMemberFunction() != c.IsMemberFunction() {
			return false
		}
		if op := b.FunctionName(); op == operators.LogicalOr || op == operators.LogicalAnd {
			return u.unifyOperands(operands(body, op), operands(e, op), root)
		}
		if b.IsMemberFunction() && !u.unify(b.Target(), c.Target(), false) {
			return false
		}
		return u.unifyAll(b.Args(), c.Args())
	case celast.ListKind:
		return u.unifyAll(body.AsList().Elements(), e.AsList().Elements())
	case celast.ComprehensionKind:
		b, c := body.AsComprehension(), e.AsComprehension()
		if b.AccuVar() != c.AccuVar() || (b.IterVar2() == "") != (c.IterVar2() == "") {
			return false
		}
		if !u.unify(b.IterRange(), c.IterRange(), false) || !u.unify(b.AccuInit(), c.AccuInit(), false) {
			return false
		}
		saved := u.bound
		u.bound = map[string]string{b.IterVar(): c.IterVar()}
		if b.IterVar2() != "" {
			u.bound[b.IterVar2()] = c.IterVar2()
		}
		for k, v := range saved {
			if _, shadowed := u.bound[k]; !shadowed {
				u.bound[k] = v
			}
		}
		ok := u.unify(b.LoopCondition(), c.LoopCondition(), false) && u.unify(b.LoopStep(), c.LoopStep(), false) && u.unify(b.Result(), c.Result(), false)
		u.bound = saved
		return ok
	}
	// Maps and message literals do not occur in a predicate over an event; the same text would.
	return sameExpr(body, e)
}

func (u *unifier) unifyAll(body, es []celast.Expr) bool {
	if len(body) != len(es) {
		return false
	}
	for i := range body {
		if !u.unify(body[i], es[i], false) {
			return false
		}
	}
	return true
}

// unifyOperands matches the operands of a body's `||` or `&&` to operands of the rule's, each
// to one of its own, in any order. Unless subset is set the rule must have no operand left.
func (u *unifier) unifyOperands(body, es []celast.Expr, subset bool) bool {
	if len(body) > len(es) || !subset && len(body) != len(es) {
		return false
	}
	used := make([]bool, len(es))
	var assign func(i int) bool
	assign = func(i int) bool {
		if i == len(body) {
			return true
		}
		for k := range es {
			if used[k] {
				continue
			}
			savedArgs := maps.Clone(u.args)
			used[k] = true
			if u.unify(body[i], es[k], false) && assign(i+1) {
				return true
			}
			used[k], u.args = false, savedArgs
		}
		return false
	}
	return assign(0)
}

// operands returns the operands of a chain of one logical operator, however the parser nested it.
func operands(e celast.Expr, op string) []celast.Expr {
	if e.Kind() != celast.CallKind || e.AsCall().FunctionName() != op {
		return []celast.Expr{e}
	}
	var out []celast.Expr
	for _, arg := range e.AsCall().Args() {
		out = append(out, operands(arg, op)...)
	}
	return out
}

// sameExpr reports whether two expressions are written the same.
func sameExpr(a, b celast.Expr) bool {
	return exprText(a) == exprText(b)
}

// exprText writes an expression's structure as text, without the numbers the parser gave it.
func exprText(e celast.Expr) string {
	var b strings.Builder
	var write func(e celast.Expr)
	list := func(es []celast.Expr) {
		for _, x := range es {
			write(x)
			b.WriteByte(',')
		}
	}
	write = func(e celast.Expr) {
		switch e.Kind() {
		case celast.IdentKind:
			b.WriteString(e.AsIdent())
		case celast.LiteralKind:
			fmt.Fprintf(&b, "%T(%v)", e.AsLiteral().Value(), e.AsLiteral().Value())
		case celast.SelectKind:
			write(e.AsSelect().Operand())
			fmt.Fprintf(&b, ".%s?%t", e.AsSelect().FieldName(), e.AsSelect().IsTestOnly())
		case celast.CallKind:
			c := e.AsCall()
			if c.IsMemberFunction() {
				write(c.Target())
				b.WriteByte('.')
			}
			b.WriteString(c.FunctionName() + "(")
			list(c.Args())
			b.WriteByte(')')
		case celast.ListKind:
			b.WriteByte('[')
			list(e.AsList().Elements())
			b.WriteByte(']')
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			fmt.Fprintf(&b, "fold(%s,%s,%s;", c.IterVar(), c.IterVar2(), c.AccuVar())
			list([]celast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()})
			b.WriteByte(')')
		default:
			// A map or a message literal: no two are taken for the same.
			fmt.Fprintf(&b, "#%d", e.ID())
		}
	}
	write(e)
	return b.String()
}
