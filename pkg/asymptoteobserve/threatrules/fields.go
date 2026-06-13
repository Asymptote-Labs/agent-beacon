package threatrules

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Field describes one CEL-addressable event field: the dotted path a rule references
// (after the "e." prefix) and its CEL type.
type Field struct {
	Path string
	Type string
}

// EventFields returns the authoritative set of CEL-addressable event fields a rule can
// match on, using the JSON field names CEL exposes. Candidate leaves are discovered by
// reflecting the Beacon event, then filtered to those that actually compile against the
// CEL env — so fields cel-go does not expose (e.g. interface{} or map[string]interface{}
// payloads) are excluded by construction, and each field's Type is its real CEL type.
func EventFields() []Field {
	var candidates []Field
	walkFields(reflect.TypeOf(asymptoteobserve.Event{}), "", map[reflect.Type]bool{}, &candidates)

	env, err := Env()
	if err != nil {
		// Env construction is exercised by its own test; degrade to reflected types.
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
		return candidates
	}

	fields := make([]Field, 0, len(candidates))
	for _, c := range candidates {
		ast, iss := env.Compile("e." + c.Path)
		if iss != nil && iss.Err() != nil {
			continue // not addressable in CEL (e.g. dyn/interface payload)
		}
		fields = append(fields, Field{Path: c.Path, Type: ast.OutputType().String()})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return fields
}

func walkFields(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]Field) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if seen[t] {
		return // guard against any cyclic type graph
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonFieldName(f)
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && !isLeafStruct(ft) {
			walkFields(ft, path, seen, out)
			continue
		}
		*out = append(*out, Field{Path: path, Type: celTypeName(ft)})
	}
}

// isLeafStruct reports structs that CEL treats as scalars rather than objects to recurse
// into. None of the Beacon event sub-types qualify today, but this keeps the walker
// honest if a time.Time or similar is added.
func isLeafStruct(t reflect.Type) bool {
	return t.String() == "time.Time"
}

// jsonFieldName returns the CEL field name for a struct field: the first segment of its
// json tag, falling back to the Go field name (mirroring cel-go's json tag handler).
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	return strings.Split(tag, ",")[0]
}

func celTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "uint"
	case reflect.Float32, reflect.Float64:
		return "double"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	case reflect.Interface:
		return "dyn"
	default:
		return "dyn"
	}
}

// RenderFieldsMarkdown renders the field reference as the committed FIELDS.md document.
// The CLI `threatrules fields` command and the drift test share this single renderer.
func RenderFieldsMarkdown() string {
	var b strings.Builder
	b.WriteString("# Threat Rules — Event field reference\n\n")
	b.WriteString("Generated from the Beacon `Event` schema. These are the fields a rule's CEL\n")
	b.WriteString("expression can reference, bound to the variable `e` (e.g. `e.event.action`).\n")
	b.WriteString("Field selection is null-safe: an absent sub-object yields the zero value.\n\n")
	b.WriteString("Regenerate with `beacon rules fields --markdown > spec/threat-rules/FIELDS.md`.\n\n")
	b.WriteString("| Field | Type |\n|---|---|\n")
	for _, f := range EventFields() {
		fmt.Fprintf(&b, "| `e.%s` | %s |\n", f.Path, f.Type)
	}
	return b.String()
}
