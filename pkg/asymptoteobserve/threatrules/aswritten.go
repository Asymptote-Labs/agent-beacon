package threatrules

import (
	celast "github.com/google/cel-go/common/ast"
)

// An expression read as written is parsed with the engine's environment alone, without the
// macros of a library: a call to a predicate stays a call, and nothing a predicate expands to is
// seen. It is how the engine finds the predicates an expression or a predicate's body calls
// (Library.uses, setAside), names the function a rule calls that its definitions lack (explain),
// and finds what a rule restates of its own pack (CatalogCopies, PredicateCopies), so that all of
// them read the same text the same way.

// asWritten parses expr as written.
func asWritten(expr string) (celast.Expr, error) {
	env, err := Env()
	if err != nil {
		return nil, err
	}
	parsed, iss := env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	return parsed.NativeRep().Expr(), nil
}

// ruleExpressions returns the expressions a rule is written with: its match, or its steps'.
func ruleExpressions(r *Rule) []string {
	if r.Correlation == nil {
		return []string{r.Match}
	}
	var exprs []string
	for _, s := range r.Correlation.Steps {
		exprs = append(exprs, s.Match)
	}
	return exprs
}

// written returns the rule's expressions as written (ruleExpressions). One that does not parse
// is left out: Validate reports it.
func (r *Rule) written() []celast.Expr {
	var written []celast.Expr
	for _, expr := range ruleExpressions(r) {
		if e, err := asWritten(expr); err == nil {
			written = append(written, e)
		}
	}
	return written
}

// matchesLiteral is a call of matches() with a constant pattern: what it matches, and the
// pattern as the expression writes it.
type matchesLiteral struct {
	subject celast.Expr
	pattern string
}

// matchesLiterals returns every call of matches() under e whose pattern is a constant string, in
// either form: s.matches("re") and matches(s, "re").
func matchesLiterals(e celast.Expr) []matchesLiteral {
	var literals []matchesLiteral
	celast.PreOrderVisit(e, celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.CallKind || e.AsCall().FunctionName() != "matches" {
			return
		}
		call := e.AsCall()
		args := call.Args()
		var subject celast.Expr
		switch {
		case call.IsMemberFunction() && len(args) == 1:
			subject = call.Target()
		case !call.IsMemberFunction() && len(args) == 2:
			subject = args[0]
		default:
			return
		}
		if pattern, ok := stringLiteral(args[len(args)-1]); ok {
			literals = append(literals, matchesLiteral{subject, pattern})
		}
	}))
	return literals
}
