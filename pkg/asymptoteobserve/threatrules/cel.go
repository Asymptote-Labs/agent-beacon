package threatrules

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/ext"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// eventVar is the CEL variable name a rule's match expression binds to.
const eventVar = "e"

// eventCELType is the CEL object type name for asymptoteobserve.Event. cel-go's
// native-types extension names a type "<lastPkgSegment>.<TypeName>", so for the Event
// type in package asymptoteobserve this is "asymptoteobserve.Event". We derive it from
// reflection rather than hardcoding so it survives a package move.
var eventCELType = celObjectTypeName(reflect.TypeOf(asymptoteobserve.Event{}))

func celObjectTypeName(t reflect.Type) string {
	segments := strings.Split(t.PkgPath(), "/")
	return segments[len(segments)-1] + "." + t.Name()
}

// nativeEnv is the environment as cel-go's native-types extension builds it. It is built once: the
// registration walks the whole Event struct tree and is comparatively expensive, and the resulting
// env is safe for concurrent use.
var nativeEnv = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		ext.NativeTypes(reflect.TypeOf(asymptoteobserve.Event{}), ext.ParseStructField(celFieldName)),
		cel.Variable(eventVar, cel.ObjectType(eventCELType)),
	)
})

// fastTypes are the event's types read by index (see fastobj.go), or nil if they cannot be built
// against nativeEnv; TestFastEnvIsInstalled guards against that.
var fastTypes = sync.OnceValue(func() *fastAdapter {
	env, err := nativeEnv()
	if err != nil {
		return nil
	}
	a, err := newFastAdapter(env)
	if err != nil {
		return nil
	}
	return a
})

// celEnv is nativeEnv with fastTypes installed, or nativeEnv itself without them.
var celEnv = sync.OnceValues(func() (*cel.Env, error) {
	env, err := nativeEnv()
	if err != nil {
		return nil, err
	}
	a := fastTypes()
	if a == nil {
		return env, nil
	}
	return env.Extend(cel.CustomTypeAdapter(a), cel.CustomTypeProvider(a.provider))
})

// Env returns the shared CEL environment that match expressions compile against.
//
// The Beacon Event is registered as a native CEL type keyed by its JSON field names, so
// expressions read like the event JSON ("e.event.action", "e.command.command",
// "e.file.path", "e.gen_ai.usage.input_tokens"). Field selection is null-safe: cel-go's
// native-types provider substitutes a zero-valued struct for any nil pointer field, so
// "e.command.command" on an event without a command sub-object evaluates to "" rather
// than erroring.
func Env() (*cel.Env, error) {
	return celEnv()
}

// celFieldName is the CEL name of an event struct field: its `cel` tag when it has one,
// otherwise the first segment of its json tag, otherwise the Go field name.
//
// The json tag is the rule for every field that is on the wire, so expressions read like the
// event JSON. The `cel` tag exists for the one kind of field that is not: a value the engine
// derives for rules to match on and never serializes (json:"-"), such as
// gen_ai.tool.call.result_text. EventFields uses the same function, so the field reference
// and the environment cannot disagree about a name.
func celFieldName(f reflect.StructField) string {
	if tag, ok := f.Tag.Lookup("cel"); ok {
		if name := strings.Split(tag, ",")[0]; name != "" {
			return name
		}
	}
	if tag, ok := f.Tag.Lookup("json"); ok {
		return strings.Split(tag, ",")[0]
	}
	return f.Name
}

// MaxEvalCost is the CEL runtime cost one evaluation of one match expression against one
// event may spend before it is stopped with an error. Cost is cel-go's deterministic
// measure of the work an expression does (comprehension iterations, string bytes
// traversed, regex size times input size), so the limit holds on every machine and is
// not a wall-clock timeout.
//
// Without it a rule could hang a scan: seven nested .all() macros over ten-element list
// literals run ten million iterations per event, about six seconds each, and nested
// .map() can exhaust memory. A rule arrives as data (`beacon rules pull`), so the engine
// cannot trust it to be cheap.
//
// The value is generous. TestCorpusStaysUnderEvalCostLimit requires every expression in the
// open corpus and the embedded baseline to spend under a hundredth of it (50,000) on its own
// fixtures, and under a fifth of it (1,000,000) on a pessimistic event whose every string
// field is 64 KiB at once (the writer caps the whole event line at 64 KiB, so no real event
// gets close). At this limit the seven-deep .all() above stops with an error after roughly
// two seconds instead of running to completion.
const MaxEvalCost uint64 = 5_000_000

// evalCostLimit is the limit CompileMatch applies; tests lower it so runaway expressions
// stop quickly.
var evalCostLimit = MaxEvalCost

// CompileMatch compiles a single match expression and verifies it types to bool. The
// returned program is safe for concurrent reuse across events, and each evaluation is
// bounded by MaxEvalCost: one that exceeds it fails with an error rather than matching. A
// compile or type-check error (including a reference to an unknown event field) is
// returned to the caller; this is what makes "every shipped rule provably compiles
// against the schema" a load-time guarantee.
//
// Constant matches() patterns are compiled once, here, and run behind a prefilter (see
// prefilter.go). Neither changes a result, an error or a CEL cost.
func CompileMatch(expr string) (cel.Program, error) {
	prog, _, err := compileMatch(expr)
	return prog, err
}

// compileMatch is CompileMatch, also returning the checked expression.
func compileMatch(expr string) (cel.Program, *cel.Ast, error) {
	return (*Library)(nil).compileMatch(expr)
}

// compileMatch compiles expr against the library's environment: the engine's, with the
// library's predicates expanded where expr calls them.
//
// The result is kept (matchCache). Loading a rule validates its expressions, compiling a rule
// validates it again and then compiles them, and each of those parsed, type-checked and
// planned the same text: three times over for every expression of a pack, which was most of
// what starting a scan cost before its first event.
func (l *Library) compileMatch(expr string) (cel.Program, *cel.Ast, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil, fmt.Errorf("empty match expression")
	}
	cache, key := l.matchCache(), matchKey{expr: expr, limit: evalCostLimit}
	if m, ok := cache.get(key); ok {
		return m.prog, m.ast, m.err
	}
	m := &compiledMatch{}
	m.prog, m.ast, m.err = l.compileMatchUncached(expr)
	cache.put(key, m)
	return m.prog, m.ast, m.err
}

func (l *Library) compileMatchUncached(expr string) (cel.Program, *cel.Ast, error) {
	env, err := l.environment()
	if err != nil {
		return nil, nil, fmt.Errorf("build CEL env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, nil, fmt.Errorf("compile match: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, nil, fmt.Errorf("match expression must evaluate to bool, got %s", ast.OutputType())
	}
	prog, err := env.Program(ast, cel.CostLimit(evalCostLimit), cel.OptimizeRegex(prefilteredMatches))
	if err != nil {
		return nil, nil, fmt.Errorf("build CEL program: %w", err)
	}
	return prog, ast, nil
}

// matchCache holds what compiling an expression gave, for one set of definitions: the program,
// the checked expression, or the error. An expression means the same every time it is
// compiled with the same definitions and the same cost limit, and a program is safe to share.
type matchCache struct {
	mu      sync.Mutex
	matches map[matchKey]*compiledMatch
}

type matchKey struct {
	expr  string
	limit uint64
}

type compiledMatch struct {
	prog cel.Program
	ast  *cel.Ast
	err  error
}

// maxCachedMatches bounds a cache. A pack has a hundred expressions or so; a process that
// compiles arbitrary ones (a linter run over many packs, a fuzzer) starts again when full.
const maxCachedMatches = 4096

// plainMatches is the cache of expressions compiled with no definitions.
var plainMatches matchCache

func (l *Library) matchCache() *matchCache {
	if l == nil {
		return &plainMatches
	}
	return &l.matches
}

func (c *matchCache) get(key matchKey) (*compiledMatch, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.matches[key]
	return m, ok
}

func (c *matchCache) put(key matchKey, m *compiledMatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.matches == nil || len(c.matches) >= maxCachedMatches {
		c.matches = map[matchKey]*compiledMatch{}
	}
	c.matches[key] = m
}

// selectsField reports whether the checked expression selects a field named name anywhere,
// as e.command.parsed_commands does. A name inside a string literal is not a selection.
func selectsField(ast *cel.Ast, name string) bool {
	found := false
	celast.PreOrderVisit(ast.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() == celast.SelectKind && e.AsSelect().FieldName() == name {
			found = true
		}
	}))
	return found
}

// EvalMatch runs a compiled match program against one event and reports whether it
// matched. A non-bool result is treated as an evaluation error.
//
// The program reads the event with its derived fields (see DerivedFields) filled, so a caller
// holding only a program sees the same values a CompiledRule does; a rule that never reads a
// derived field skips that step, and a scan does it once per event for all the rules that do.
// It is evaluated as a scan evaluates a rule (expressions.evaluate), with no action to skip.
func EvalMatch(prog cel.Program, event asymptoteobserve.Event) (bool, error) {
	return evalAlone(matcher{prog: prog, derived: true}, event)
}

func evalMatch(prog cel.Program, act *eventActivation) (bool, error) {
	out, _, err := prog.Eval(act)
	if err != nil {
		return false, fmt.Errorf("eval match: %w", err)
	}
	matched, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("match expression returned %T, want bool", out.Value())
	}
	return matched, nil
}
