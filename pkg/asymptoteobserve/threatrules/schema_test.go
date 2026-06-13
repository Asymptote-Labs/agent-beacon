package threatrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// schemaNode is the subset of JSON Schema we read back from spec/threat-rules/schema.json.
type schemaNode struct {
	Pattern    string                `json:"pattern"`
	Enum       []string              `json:"enum"`
	Required   []string              `json:"required"`
	Properties map[string]schemaNode `json:"properties"`
	Items      *schemaNode           `json:"items"`
}

// TestSchemaJSONInSyncWithGo guards the hand-maintained spec/threat-rules/schema.json
// (the artifact external, non-Go tooling validates against) from drifting away from the
// authoritative Go validator. It does not run a full JSON Schema validation (we
// deliberately add no jsonschema dependency); it asserts the things that realistically
// drift: the property set, the enums, the id pattern, and the required fields.
func TestSchemaJSONInSyncWithGo(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(specDir(t), "schema.json"))
	if err != nil {
		t.Fatalf("read schema.json: %v", err)
	}
	var schema schemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema.json: %v", err)
	}

	// Property set must equal the Rule struct's yaml field set.
	gotProps := make([]string, 0, len(schema.Properties))
	for k := range schema.Properties {
		gotProps = append(gotProps, k)
	}
	assertSameSet(t, "top-level properties", ruleYAMLFields(), gotProps)

	// Required fields.
	assertSameSet(t, "required",
		[]string{"id", "version", "title", "severity", "status", "posture", "emit", "tests"},
		schema.Required)

	// id pattern must match the Go validator's regexp.
	if got := schema.Properties["id"].Pattern; got != idPattern.String() {
		t.Errorf("id pattern: schema %q != Go %q", got, idPattern.String())
	}

	// Enums must match the authoritative Go constants.
	assertSameSet(t, "severity enum",
		[]string{"info", "low", "medium", "high", "critical"},
		schema.Properties["severity"].Enum)
	assertSameSet(t, "status enum",
		[]string{string(StatusExperimental), string(StatusStable), string(StatusDeprecated)},
		schema.Properties["status"].Enum)
	assertSameSet(t, "posture enum",
		[]string{string(PostureDetect), string(PostureEnforceCapable)},
		schema.Properties["posture"].Enum)
	assertSameSet(t, "correlation.scope enum",
		[]string{string(ScopeSession)},
		schema.Properties["correlation"].Properties["scope"].Enum)

	testsItems := schema.Properties["tests"].Items
	if testsItems == nil {
		t.Fatal("schema.json: tests.items missing")
	}
	assertSameSet(t, "tests.verdict enum",
		[]string{string(VerdictMatch), string(VerdictNoMatch)},
		testsItems.Properties["verdict"].Enum)
}

// ruleYAMLFields returns the first segment of every yaml tag on the Rule struct.
func ruleYAMLFields() []string {
	rt := reflect.TypeOf(Rule{})
	fields := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		fields = append(fields, strings.Split(tag, ",")[0])
	}
	return fields
}

func assertSameSet(t *testing.T, label string, want, got []string) {
	t.Helper()
	ws := append([]string(nil), want...)
	gs := append([]string(nil), got...)
	sort.Strings(ws)
	sort.Strings(gs)
	if !reflect.DeepEqual(ws, gs) {
		t.Errorf("%s mismatch:\n  Go/expected: %v\n  schema.json: %v", label, ws, gs)
	}
}
