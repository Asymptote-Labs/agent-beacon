package threatrules

import (
	"reflect"
	"sort"
	"sync"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// specField is an event field that a rule may select only when it declares the spec version
// that added the field. The list is read from the `spec` struct tags of the event types
// (asymptoteobserve.CommandInfo.ParsedCommands, ParsedIncomplete and ParsedCommand's fields), so
// declaring a field with its version is all it takes: Validate holds rules to it, and the field
// reference and TestSpecFieldsAreDocumented hold the documentation to it.
type specField struct {
	// name is the field's CEL name, what an expression selects.
	name string
	// path is the CEL path after "e.", with [] for the entries of a list:
	// command.parsed_commands[].args.
	path string
	// version is the tag as written (v1.3), and spec the declaration it requires
	// (threat-rules/v1.3).
	version string
	spec    string
	since   specVersion
}

// specFields returns the version-tagged event fields, newest version first and in declaration
// order within a version, so the first one an expression selects names the version it needs.
var specFields = sync.OnceValue(func() []specField {
	var out []specField
	collectSpecFields(reflect.TypeOf(asymptoteobserve.Event{}), "", map[reflect.Type]bool{}, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].since.newerThan(out[j].since) })
	return out
})

func collectSpecFields(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]specField) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice {
			prefix += "[]"
		}
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
		if version, ok := f.Tag.Lookup("spec"); ok {
			spec := "threat-rules/" + version
			since, err := parseSpec(spec)
			if err != nil {
				panic("threatrules: spec tag of " + path + ": " + err.Error())
			}
			*out = append(*out, specField{name: name, path: path, version: version, spec: spec, since: since})
		}
		collectSpecFields(f.Type, path, seen, out)
	}
}
