package threatrules

import (
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// DerivedFields returns every engine-derived field as a CEL path after "e.": an event field a rule
// can address (it has a `cel` name) that is never on the wire (`json:"-"`).
//
// The list is read from the event schema, so declaring a field that way is what makes it derived.
// Compile decides from these names which rules pay for derivation, the field reference documents
// each one from derivedFieldDocs, and the tests require each to be documented in SPEC.md and
// cleared by withDerivedFields when a caller asserts it.
func DerivedFields() []string {
	return derivedFields()
}

var derivedFields = sync.OnceValue(func() []string {
	var out []string
	collectDerivedFields(reflect.TypeOf(asymptoteobserve.Event{}), "", map[reflect.Type]bool{}, &out)
	sort.Strings(out)
	return out
})

func collectDerivedFields(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)
	for i := range t.NumField() {
		f := t.Field(i)
		name := celFieldName(f)
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if f.Tag.Get("json") == "-" {
			*out = append(*out, path)
			continue
		}
		collectDerivedFields(f.Type, path, seen, out)
	}
}

// derivedFieldNames is the last segment of each derived path, the only way an expression can
// reach the field.
var derivedFieldNames = sync.OnceValue(func() []string {
	paths := DerivedFields()
	names := make([]string, len(paths))
	for i, path := range paths {
		names[i] = path[strings.LastIndex(path, ".")+1:]
	}
	return names
})

// referencesDerivedField reports whether a match expression names an engine-derived field.
// A textual check is enough: a false positive (the name inside a string literal) only costs
// the derivation, and a false negative cannot happen because CEL has no other way to reach
// the field.
func referencesDerivedField(expr string) bool {
	for _, name := range derivedFieldNames() {
		if strings.Contains(expr, name) {
			return true
		}
	}
	return false
}
