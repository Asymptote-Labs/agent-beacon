package threatrules

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFieldsAreAddressable proves the generated field reference is accurate against the
// real CEL env: every documented path must compile, and scalar paths must report the
// documented type. This is what keeps FIELDS.md from listing a field CEL would reject.
func TestFieldsAreAddressable(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	fields := EventFields()
	if len(fields) == 0 {
		t.Fatal("EventFields() returned nothing")
	}
	scalarTypes := map[string]bool{"string": true, "int": true, "uint": true, "double": true, "bool": true}
	for _, f := range fields {
		ast, iss := env.Compile("e." + f.Path)
		if iss != nil && iss.Err() != nil {
			t.Errorf("field e.%s does not compile: %v", f.Path, iss.Err())
			continue
		}
		if scalarTypes[f.Type] && ast.OutputType().String() != f.Type {
			t.Errorf("field e.%s documented as %s but CEL types it as %s", f.Path, f.Type, ast.OutputType())
		}
	}
}

// TestFieldsDocInSync asserts the committed FIELDS.md matches the generated reference, so
// adding a field to the Event schema fails CI until the doc is regenerated.
func TestFieldsDocInSync(t *testing.T) {
	path := filepath.Join(specDir(t), "FIELDS.md")
	want := RenderFieldsMarkdown()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read FIELDS.md (regenerate: beacon rules fields --markdown > spec/threat-rules/FIELDS.md): %v", err)
	}
	if string(got) != want {
		t.Fatalf("FIELDS.md is stale; regenerate with: beacon rules fields --markdown > spec/threat-rules/FIELDS.md")
	}
}
