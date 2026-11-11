package threatrules

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	celast "github.com/google/cel-go/common/ast"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/concepts"
)

// A predicate is a named expression a rule calls like a function: credential_path(e.file.path).
// It is expanded where it is called, before the expression is checked, so the engine evaluates,
// indexes and bounds a rule that calls one exactly as it would the same rule with the
// predicate's body written out. Nothing about a predicate exists at evaluation time.
//
// A predicate is defined in one of two ways. A definition (a *.predicates.yaml file) gives a
// body over named parameters. A catalog (a *.catalog.yaml file) provides one for its paths and
// one for its commands, whose body is generated from the catalog's stores.
type predicate struct {
	name string
	// source says where the predicate is defined, for error messages.
	source string
	// params and text are a definition's parameters and body as written; body is the body parsed,
	// with its own variables renamed (see hygienic). uses are the predicates the body calls.
	params []string
	text   string
	body   celast.Expr
	uses   []string
	// catalog and commands are set for a catalog's predicate: over its paths, or its commands.
	catalog  *concepts.Catalog
	commands bool
	// selections caches the catalog predicate's parsed body for each tier and kinds it is
	// called with.
	mu         sync.Mutex
	selections map[string]celast.Expr
}

// predicateNamePattern is the form of a predicate's name and of a parameter's.
var (
	predicateNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	parameterPattern     = regexp.MustCompile(`^[a-z_][a-zA-Z0-9_]*$`)
)

// reservedNames cannot name a predicate: CEL's own functions, macros and reserved words, and
// the event variable.
var reservedNames = []string{
	eventVar, "has", "all", "exists", "exists_one", "map", "filter", "size", "matches", "contains",
	"startsWith", "endsWith", "int", "uint", "double", "string", "bool", "bytes", "duration",
	"timestamp", "type", "dyn", "true", "false", "null", "in",
	"as", "break", "const", "continue", "else", "for", "function", "if", "import", "let", "loop",
	"package", "namespace", "return", "var", "void", "while",
}

// subjectParam is the parameter of a catalog predicate's generated body: what it is asked about.
const subjectParam = "subject"

// macro returns the predicate as a CEL macro for every argument count, so that a call with the
// wrong number of arguments is told so rather than reported as an unknown function.
func (p *predicate) macro() cel.Macro {
	return cel.GlobalVarArgMacro(p.name, func(eh cel.MacroExprFactory, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		fail := func(format string, a ...any) (celast.Expr, *common.Error) {
			var id int64
			if len(args) > 0 {
				id = args[0].ID()
			}
			return nil, eh.NewError(id, p.name+": "+fmt.Sprintf(format, a...))
		}
		if p.catalog != nil {
			if len(args) == 0 {
				return fail("takes what it is asked about, then optionally a tier and kinds")
			}
			tier := concepts.TierLikely
			var kinds []string
			for i, arg := range args[1:] {
				text, ok := stringLiteral(arg)
				if !ok {
					return fail("a tier and a kind are written as string literals, so that the rule says which stores it means")
				}
				if i == 0 {
					tier = concepts.Tier(text)
				} else {
					kinds = append(kinds, text)
				}
			}
			body, err := p.selection(tier, kinds)
			if err != nil {
				return fail("%v", err)
			}
			return substitute(eh, body, []string{subjectParam}, args[:1]), nil
		}
		if len(args) != len(p.params) {
			return fail("takes %d argument(s) (%s), got %d", len(p.params), strings.Join(p.params, ", "), len(args))
		}
		if p.body == nil {
			return fail("is not resolved: its definition did not load")
		}
		return substitute(eh, p.body, p.params, args), nil
	})
}

// selection returns the body of a catalog predicate for a tier and kinds: the catalog's
// expression for that selection, over subjectParam.
func (p *predicate) selection(tier concepts.Tier, kinds []string) (celast.Expr, error) {
	key := string(tier) + "\x00" + strings.Join(kinds, "\x00")
	p.mu.Lock()
	defer p.mu.Unlock()
	if body, ok := p.selections[key]; ok {
		return body, nil
	}
	selection, err := p.catalog.Select(tier, kinds...)
	if err != nil {
		return nil, err
	}
	text := selection.PathCEL(subjectParam)
	if p.commands {
		text = selection.CommandCEL(subjectParam)
	}
	env, err := Env()
	if err != nil {
		return nil, err
	}
	parsed, iss := env.Parse(text)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("the expression generated from catalog %s does not parse: %w", p.catalog.Name, iss.Err())
	}
	body := parsed.NativeRep().Expr()
	if p.selections == nil {
		p.selections = map[string]celast.Expr{}
	}
	p.selections[key] = body
	return body, nil
}

// substitute returns body built anew at the call, with each parameter replaced by a copy of its
// argument. An argument is put in place and not read again, so one that names another parameter
// is not replaced in turn. Every expression of the body is made where the call is, so an error
// inside it is reported at the call rather than at whatever part of the calling expression
// happens to share a number with it.
func substitute(eh cel.MacroExprFactory, body celast.Expr, params []string, args []celast.Expr) celast.Expr {
	var build func(e celast.Expr) celast.Expr
	all := func(exprs []celast.Expr) []celast.Expr {
		out := make([]celast.Expr, len(exprs))
		for i, e := range exprs {
			out[i] = build(e)
		}
		return out
	}
	build = func(e celast.Expr) celast.Expr {
		switch e.Kind() {
		case celast.IdentKind:
			if k := slices.Index(params, e.AsIdent()); k >= 0 {
				return eh.Copy(args[k])
			}
			return eh.NewIdent(e.AsIdent())
		case celast.LiteralKind:
			return eh.NewLiteral(e.AsLiteral())
		case celast.SelectKind:
			sel := e.AsSelect()
			if sel.IsTestOnly() {
				return eh.NewPresenceTest(build(sel.Operand()), sel.FieldName())
			}
			return eh.NewSelect(build(sel.Operand()), sel.FieldName())
		case celast.CallKind:
			call := e.AsCall()
			if call.IsMemberFunction() {
				return eh.NewMemberCall(call.FunctionName(), build(call.Target()), all(call.Args())...)
			}
			return eh.NewCall(call.FunctionName(), all(call.Args())...)
		case celast.ListKind:
			return eh.NewList(all(e.AsList().Elements())...)
		case celast.MapKind:
			entries := e.AsMap().Entries()
			out := make([]celast.EntryExpr, len(entries))
			for i, entry := range entries {
				m := entry.AsMapEntry()
				out[i] = eh.NewMapEntry(build(m.Key()), build(m.Value()), m.IsOptional())
			}
			return eh.NewMap(out...)
		case celast.StructKind:
			fields := e.AsStruct().Fields()
			out := make([]celast.EntryExpr, len(fields))
			for i, field := range fields {
				f := field.AsStructField()
				out[i] = eh.NewStructField(f.Name(), build(f.Value()), f.IsOptional())
			}
			return eh.NewStruct(e.AsStruct().TypeName(), out...)
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			return eh.NewComprehensionTwoVar(build(c.IterRange()), c.IterVar(), c.IterVar2(), c.AccuVar(),
				build(c.AccuInit()), build(c.LoopCondition()), build(c.LoopStep()), build(c.Result()))
		}
		return eh.Copy(e)
	}
	return build(body)
}

// hygienic prepares a definition's parsed body for expansion. It renames the variables the body
// binds itself (the c of x.exists(c, ..)) to names no rule can write, so that an argument
// substituted under them is never captured: with reads(pc) defined as pc.file_reads.exists(f, ..),
// the call reads(f) inside a rule's own exists(f, ..) still means the rule's f. It returns the
// names the body reads without binding them, which must be its parameters.
func hygienic(name string, body celast.Expr) (free []string) {
	r := renamer{prefix: "@" + name + "_", fac: celast.NewExprFactory(), free: map[string]bool{}}
	r.rename(body, nil)
	free = slices.Collect(maps.Keys(r.free))
	slices.Sort(free)
	return free
}

type renamer struct {
	prefix string
	next   int
	fac    celast.ExprFactory
	free   map[string]bool
}

func (r *renamer) fresh() string {
	r.next++
	return fmt.Sprintf("%s%d", r.prefix, r.next)
}

// rename walks e with bound, the variables in scope and what each is renamed to.
func (r *renamer) rename(e celast.Expr, bound map[string]string) {
	switch e.Kind() {
	case celast.IdentKind:
		if to, ok := bound[e.AsIdent()]; ok {
			e.SetKindCase(r.fac.NewIdent(e.ID(), to))
		} else {
			r.free[e.AsIdent()] = true
		}
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		// The range and the accumulator's initial value are outside the comprehension's scope; the
		// condition and the step see the iteration variables and the accumulator; the result sees
		// the accumulator.
		r.rename(c.IterRange(), bound)
		r.rename(c.AccuInit(), bound)
		outer := maps.Clone(bound)
		if outer == nil {
			outer = map[string]string{}
		}
		outer[c.AccuVar()] = c.AccuVar()
		inner := maps.Clone(outer)
		iterVar, iterVar2 := r.fresh(), ""
		inner[c.IterVar()] = iterVar
		if c.HasIterVar2() {
			iterVar2 = r.fresh()
			inner[c.IterVar2()] = iterVar2
		}
		r.rename(c.LoopCondition(), inner)
		r.rename(c.LoopStep(), inner)
		r.rename(c.Result(), outer)
		e.SetKindCase(r.fac.NewComprehensionTwoVar(e.ID(), c.IterRange(), iterVar, iterVar2, c.AccuVar(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()))
	default:
		children(e, func(child celast.Expr) { r.rename(child, bound) })
	}
}

// walkExpr calls visit for e and every expression under it.
func walkExpr(e celast.Expr, visit func(celast.Expr)) {
	visit(e)
	children(e, func(child celast.Expr) { walkExpr(child, visit) })
}

// children calls visit for each expression directly under e.
func children(e celast.Expr, visit func(celast.Expr)) {
	switch e.Kind() {
	case celast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			visit(call.Target())
		}
		for _, arg := range call.Args() {
			visit(arg)
		}
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		for _, part := range []celast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()} {
			visit(part)
		}
	case celast.ListKind:
		for _, element := range e.AsList().Elements() {
			visit(element)
		}
	case celast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			visit(entry.AsMapEntry().Key())
			visit(entry.AsMapEntry().Value())
		}
	case celast.StructKind:
		for _, field := range e.AsStruct().Fields() {
			visit(field.AsStructField().Value())
		}
	case celast.SelectKind:
		visit(e.AsSelect().Operand())
	}
}

// calls returns the names of the global functions e calls, each once, in the order met.
func calls(e celast.Expr) []string {
	var names []string
	walkExpr(e, func(e celast.Expr) {
		if e.Kind() == celast.CallKind && !e.AsCall().IsMemberFunction() && !slices.Contains(names, e.AsCall().FunctionName()) {
			names = append(names, e.AsCall().FunctionName())
		}
	})
	return names
}
