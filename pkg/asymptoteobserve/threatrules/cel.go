package threatrules

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
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

// celEnv is the shared, immutable CEL environment for compiling match expressions.
// It is built once: the native-type registration walks the whole Event struct tree and
// is comparatively expensive, and the resulting env is safe for concurrent use.
var (
	celEnvOnce sync.Once
	celEnvInst *cel.Env
	celEnvErr  error
)

// Env returns the shared CEL environment that match expressions compile against.
//
// The Beacon Event is registered as a native CEL type keyed by its JSON field names, so
// expressions read like the event JSON ("e.event.action", "e.command.command",
// "e.file.path", "e.gen_ai.usage.input_tokens"). Field selection is null-safe: cel-go's
// native-types provider substitutes a zero-valued struct for any nil pointer field, so
// "e.command.command" on an event without a command sub-object evaluates to "" rather
// than erroring.
func Env() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		celEnvInst, celEnvErr = cel.NewEnv(
			ext.NativeTypes(reflect.TypeOf(asymptoteobserve.Event{}), ext.ParseStructField(celFieldName)),
			cel.Variable(eventVar, cel.ObjectType(eventCELType)),
		)
	})
	return celEnvInst, celEnvErr
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

// CompileMatch compiles a single match expression and verifies it types to bool. The
// returned program is safe for concurrent reuse across events. A compile or type-check
// error (including a reference to an unknown event field) is returned to the caller; this
// is what makes "every shipped rule provably compiles against the schema" a load-time
// guarantee.
func CompileMatch(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("empty match expression")
	}
	env, err := Env()
	if err != nil {
		return nil, fmt.Errorf("build CEL env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("compile match: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("match expression must evaluate to bool, got %s", ast.OutputType())
	}
	prog, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("build CEL program: %w", err)
	}
	return prog, nil
}

// EvalMatch runs a compiled match program against one event and reports whether it
// matched. A non-bool result is treated as an evaluation error.
//
// Derived fields (gen_ai.tool.call.result_text) are filled on a copy of the event first, so
// a caller holding only a program sees the same values a CompiledRule does. CompiledRule
// skips that step for rules that never reference a derived field.
func EvalMatch(prog cel.Program, event asymptoteobserve.Event) (bool, error) {
	return evalMatch(prog, withDerivedFields(event))
}

func evalMatch(prog cel.Program, event asymptoteobserve.Event) (bool, error) {
	out, _, err := prog.Eval(map[string]any{eventVar: event})
	if err != nil {
		return false, fmt.Errorf("eval match: %w", err)
	}
	matched, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("match expression returned %T, want bool", out.Value())
	}
	return matched, nil
}
