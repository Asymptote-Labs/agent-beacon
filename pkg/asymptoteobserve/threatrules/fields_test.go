package threatrules

import (
	"os"
	"path/filepath"
	"strings"
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
	// Compared after normalizing line endings, because the contract is which fields the document
	// lists -- not how the checkout happens to represent newlines. The file is committed with LF,
	// but Git for Windows converts text files to CRLF on checkout by default, so a byte comparison
	// reported a perfectly current document as stale on Windows and sent the reader off to
	// regenerate a file that needed nothing.
	//
	// This does not weaken the check: a field added, removed or retyped still changes the content
	// and still fails.
	if normalizeNewlines(string(got)) != normalizeNewlines(want) {
		t.Fatalf("FIELDS.md is stale; regenerate with: beacon rules fields --markdown > spec/threat-rules/FIELDS.md")
	}
}

// normalizeNewlines makes a comparison insensitive to checkout line-ending conversion.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
