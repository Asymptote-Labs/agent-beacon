package threatrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/concepts"
)

// schemaNode is the subset of JSON Schema we read back from the schemas in spec/threat-rules.
type schemaNode struct {
	Pattern    string                `json:"pattern"`
	Enum       []string              `json:"enum"`
	Required   []string              `json:"required"`
	Properties map[string]schemaNode `json:"properties"`
	Items      *schemaNode           `json:"items"`
	Defs       map[string]schemaNode `json:"$defs"`
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
	assertSameSet(t, "correlation.order enum",
		[]string{string(OrderSequence), string(OrderAny)},
		schema.Properties["correlation"].Properties["order"].Enum)

	testsItems := schema.Properties["tests"].Items
	if testsItems == nil {
		t.Fatal("schema.json: tests.items missing")
	}
	assertSameSet(t, "tests.verdict enum",
		[]string{string(VerdictMatch), string(VerdictNoMatch)},
		testsItems.Properties["verdict"].Enum)
}

// ruleYAMLFields returns the first segment of every yaml tag on the Rule struct.
func ruleYAMLFields() []string { return yamlFields(reflect.TypeOf(Rule{})) }

// yamlFields returns the first segment of every yaml tag on a struct type.
func yamlFields(rt reflect.Type) []string {
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

func readSchema(t *testing.T, name string) schemaNode {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(specDir(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var schema schemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return schema
}

func propertyNames(n schemaNode) []string {
	names := make([]string, 0, len(n.Properties))
	for k := range n.Properties {
		names = append(names, k)
	}
	return names
}

// TestDefinitionsSchemasInSyncWithGo guards the hand-maintained predicates.schema.json and
// catalog.schema.json as TestSchemaJSONInSyncWithGo guards the rule schema: every object's
// property set is its Go type's yaml fields, and the name patterns and enums are the ones the
// decoders check.
func TestDefinitionsSchemasInSyncWithGo(t *testing.T) {
	p := readSchema(t, "predicates.schema.json")
	assertSameSet(t, "predicates file properties", yamlFields(reflect.TypeOf(predicatesFile{})), propertyNames(p))
	definition := p.Properties["definitions"].Items
	if definition == nil || definition.Properties["tests"].Items == nil || definition.Properties["params"].Items == nil {
		t.Fatal("predicates.schema.json: definitions.items, its tests.items or its params.items is missing")
	}
	assertSameSet(t, "definition properties", yamlFields(reflect.TypeOf(PredicateDefinition{})), propertyNames(*definition))
	test := definition.Properties["tests"].Items
	assertSameSet(t, "definition test properties", yamlFields(reflect.TypeOf(PredicateTest{})), propertyNames(*test))
	assertSameSet(t, "definition test verdict enum", []string{string(VerdictMatch), string(VerdictNoMatch)}, test.Properties["verdict"].Enum)
	for _, c := range []struct{ what, schema, goPattern string }{
		{"library", p.Properties["library"].Pattern, idPattern.String()},
		{"definition name", definition.Properties["name"].Pattern, predicateNamePattern.String()},
		{"param", definition.Properties["params"].Items.Pattern, parameterPattern.String()},
	} {
		if c.schema != c.goPattern {
			t.Errorf("predicates.schema.json %s pattern %q, Go %q", c.what, c.schema, c.goPattern)
		}
	}

	c := readSchema(t, "catalog.schema.json")
	assertSameSet(t, "catalog properties", yamlFields(reflect.TypeOf(concepts.Catalog{})), propertyNames(c))
	assertSameSet(t, "catalog predicates properties", yamlFields(reflect.TypeOf(concepts.Predicates{})), propertyNames(c.Properties["predicates"]))
	kind, store := c.Properties["kinds"].Items, c.Properties["stores"].Items
	if kind == nil || store == nil || store.Properties["commands"].Items == nil {
		t.Fatal("catalog.schema.json: kinds.items, stores.items or stores.items.commands.items is missing")
	}
	assertSameSet(t, "catalog kind properties", yamlFields(reflect.TypeOf(concepts.Kind{})), propertyNames(*kind))
	assertSameSet(t, "catalog store properties", yamlFields(reflect.TypeOf(concepts.Store{})), propertyNames(*store))
	assertSameSet(t, "catalog command properties", yamlFields(reflect.TypeOf(concepts.CommandMatch{})), propertyNames(*store.Properties["commands"].Items))
	assertSameSet(t, "catalog tier enum", []string{string(concepts.TierDefinite), string(concepts.TierLikely)}, store.Properties["tier"].Enum)
	for _, n := range []struct{ what, schema, goPattern string }{
		{"catalog", c.Properties["catalog"].Pattern, idPattern.String()},
		{"kind id", kind.Properties["id"].Pattern, idPattern.String()},
		{"store id", store.Properties["id"].Pattern, idPattern.String()},
		{"predicate name", c.Defs["predicateName"].Pattern, predicateNamePattern.String()},
	} {
		if n.schema != n.goPattern {
			t.Errorf("catalog.schema.json %s pattern %q, Go %q", n.what, n.schema, n.goPattern)
		}
	}
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
