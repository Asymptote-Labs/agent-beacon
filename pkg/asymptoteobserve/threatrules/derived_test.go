package threatrules

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestDerivedFieldsAreReadFromTheSchema(t *testing.T) {
	want := []string{SystemInstructionsTextPath, ToolResultTextPath}
	if got := DerivedFields(); !slices.Equal(got, want) {
		t.Fatalf("DerivedFields = %q, want %q", got, want)
	}
	for _, path := range DerivedFields() {
		name := path[strings.LastIndex(path, ".")+1:]
		if !referencesDerivedField("e." + path + `.contains("x")`) {
			t.Errorf("a rule naming %s is not marked as needing derivation", name)
		}
	}
	if referencesDerivedField(`e.gen_ai.system_instructions == null || e.prompt.text == "x"`) {
		t.Error("a rule naming no derived field is marked as needing derivation")
	}
}

func TestEveryDerivedFieldIsDocumented(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join(specDir(t), "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range DerivedFields() {
		if derivedFieldDocs[path] == "" {
			t.Errorf("%s has no entry in derivedFieldDocs", path)
		}
		if !strings.Contains(string(spec), "`e."+path+"`") {
			t.Errorf("SPEC.md does not define the derivation of e.%s", path)
		}
	}
	for path := range derivedFieldDocs {
		if !slices.Contains(DerivedFields(), path) {
			t.Errorf("derivedFieldDocs documents %s, which is not a derived field", path)
		}
	}
}

// A derived field is computed, never asserted: whatever an in-process caller put there is cleared,
// for every field the schema declares derived, including one added after this test was written.
func TestEveryDerivedFieldIsClearedWhenAsserted(t *testing.T) {
	for _, path := range DerivedFields() {
		t.Run(path, func(t *testing.T) {
			event := asymptoteobserve.Event{}
			setDerived(t, reflect.ValueOf(&event).Elem(), strings.Split(path, "."), "asserted")
			derived := withDerivedFields(event)
			if got := derivedValue(reflect.ValueOf(derived), strings.Split(path, ".")); got != "" {
				t.Fatalf("withDerivedFields kept a caller-asserted %s = %q", path, got)
			}
		})
	}
}

func fieldByCELName(v reflect.Value, name string) reflect.Value {
	for i := range v.NumField() {
		if celFieldName(v.Type().Field(i)) == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

func setDerived(t *testing.T, v reflect.Value, segments []string, value string) {
	t.Helper()
	field := fieldByCELName(v, segments[0])
	if !field.IsValid() {
		t.Fatalf("no field %q on %s", segments[0], v.Type())
	}
	if len(segments) == 1 {
		field.SetString(value)
		return
	}
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		field = field.Elem()
	}
	setDerived(t, field, segments[1:], value)
}

func derivedValue(v reflect.Value, segments []string) string {
	for _, segment := range segments {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return ""
			}
			v = v.Elem()
		}
		if v = fieldByCELName(v, segment); !v.IsValid() {
			return ""
		}
	}
	return v.String()
}
