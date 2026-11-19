package threatrules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// selectingExpr is a match expression that selects the field at path (after "e.", [] for the
// entries of a list) and nothing newer, whatever the field's type.
func selectingExpr(path string) string {
	if list, field, ok := strings.Cut(path, "[]."); ok {
		return fmt.Sprintf("e.%s.exists(c, c.%s == c.%s)", list, field, field)
	}
	return fmt.Sprintf("e.%s == e.%s", path, path)
}

// A rule that selects a version-tagged field must declare that version: every tagged field,
// including one added after this test was written, is refused below its version and accepted at
// it.
func TestRulesDeclareTheSpecOfEveryFieldTheySelect(t *testing.T) {
	if len(specFields()) == 0 {
		t.Fatal("no event field carries a spec tag")
	}
	for _, f := range specFields() {
		t.Run(f.path, func(t *testing.T) {
			expr := selectingExpr(f.path)
			validate := func(specLine string) error {
				t.Helper()
				rule, err := DecodeRule([]byte(strings.Replace(specRule("r", specLine, ""),
					"match: 'e.event.action == \"file.read\"'\n", "match: '"+expr+"'\n", 1)))
				if err != nil {
					t.Fatalf("%q: decode: %v", specLine, err)
				}
				return rule.Validate()
			}
			older := []string{"", "spec: threat-rules/v1"}
			if f.since.minor > 0 {
				older = append(older, fmt.Sprintf("spec: threat-rules/v%d.%d", f.since.major, f.since.minor-1))
			}
			// The error names the field's own version, not an older one an enclosing field needs.
			want := "e." + f.path + " requires spec: " + f.spec + " or later"
			for _, line := range older {
				if err := validate(line); err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%q: want %q, got %v", line, want, err)
				}
			}
			for _, line := range []string{"spec: " + f.spec, "spec: " + SupportedSpec} {
				if err := validate(line); err != nil {
					t.Errorf("%q: %v", line, err)
				}
			}
		})
	}
	// Naming a field in a string is not selecting it.
	rule, err := DecodeRule([]byte(strings.Replace(specRule("r", "", ""), `e.event.action == "file.read"`, `e.command.command != "parsed_commands"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rule.Validate(); err != nil {
		t.Errorf("a string literal required the spec: %v", err)
	}
}

// The fields parsed_commands came with (v1.2) carry no spec tag; every field added to a parsed
// command since must carry the version that added it, or an older engine's rejection of the field
// would reach a rule that declared an older spec instead of setting the rule aside.
func TestParsedCommandFieldsAddedLaterCarryTheirSpec(t *testing.T) {
	original := []string{"name", "argv", "argv_raw", "env", "file_effects", "pipeline", "stage", "nested"}
	entry := reflect.TypeOf(asymptoteobserve.ParsedCommand{})
	for i := range entry.NumField() {
		f := entry.Field(i)
		name := celFieldName(f)
		_, tagged := f.Tag.Lookup("spec")
		switch {
		case !tagged && !slices.Contains(original, name):
			t.Errorf("ParsedCommand.%s (%s) was added after parsed_commands: tag it with the spec version that adds it", f.Name, name)
		case tagged && slices.Contains(original, name):
			t.Errorf("ParsedCommand.%s (%s) came with parsed_commands and needs no spec tag", f.Name, name)
		}
	}
}

// Every field of a parsed command has a row in the field reference, and the rule YAML docs' row
// for e.command.parsed_commands names each.
func TestEveryParsedCommandFieldIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "detections", "yaml-schema.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	row := fieldRows(string(doc))["e."+ParsedCommandsPath]
	entry := reflect.TypeOf(asymptoteobserve.ParsedCommand{})
	var names []string
	for i := range entry.NumField() {
		name := celFieldName(entry.Field(i))
		names = append(names, name)
		if parsedCommandFieldDocs[name] == "" {
			t.Errorf("%s has no entry in parsedCommandFieldDocs", name)
		}
		if !strings.Contains(row, "`"+name+"`") {
			t.Errorf("docs/detections/yaml-schema.mdx's row for e.%s does not name %s", ParsedCommandsPath, name)
		}
	}
	for name := range parsedCommandFieldDocs {
		if !slices.Contains(names, name) {
			t.Errorf("parsedCommandFieldDocs documents %s, which a parsed command does not have", name)
		}
	}
}

// The documents that say which spec a field needs say it for every version-tagged field: SPEC.md's
// `spec` row and changelog, the JSON Schema's `spec` description, and the field table of the
// rule YAML docs, whose row for the field names the declaration it requires.
func TestSpecFieldsAreDocumented(t *testing.T) {
	read := func(parts ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	spec := read("spec", "threat-rules", "SPEC.md")
	specRow := lineStarting(t, spec, "| `spec` |")
	var schema struct {
		Properties struct {
			Spec struct {
				Description string `json:"description"`
			} `json:"spec"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(read("spec", "threat-rules", "schema.json")), &schema); err != nil {
		t.Fatal(err)
	}
	docRows := fieldRows(read("docs", "detections", "yaml-schema.mdx"))
	for _, f := range specFields() {
		// A top-level field is named by its path, an entry's field by its name.
		token := "`e." + f.path + "`"
		if strings.Contains(f.path, "[]") {
			token = "`" + f.name + "`"
		}
		if !strings.Contains(specRow, token) {
			t.Errorf("SPEC.md's spec row does not name %s", token)
		}
		if entry := changelogEntry(spec, f.version); !strings.Contains(entry, token) {
			t.Errorf("SPEC.md's changelog entry for %s does not name %s", f.version, token)
		}
		if !regexp.MustCompile(`\b` + f.name + `\b`).MatchString(schema.Properties.Spec.Description) {
			t.Errorf("schema.json's spec description does not name %s", f.name)
		}
		row, ok := docRows["e."+f.path]
		switch {
		case !ok:
			t.Errorf("docs/detections/yaml-schema.mdx has no row for e.%s", f.path)
		case !strings.Contains(row, "Requires `spec: "+f.spec+"`"):
			t.Errorf("docs/detections/yaml-schema.mdx's row for e.%s does not say it requires spec: %s", f.path, f.spec)
		}
	}
}

func lineStarting(t *testing.T, text, prefix string) string {
	t.Helper()
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starts with %q", prefix)
	return ""
}

// changelogEntry is SPEC.md's changelog bullet for version, up to the next bullet.
func changelogEntry(spec, version string) string {
	_, rest, ok := strings.Cut(spec, "\n- **"+version+"**:")
	if !ok {
		return ""
	}
	entry, _, _ := strings.Cut(rest, "\n- **")
	return entry
}

// fieldRows maps each field path a markdown table row names in its first cell (`e.a.b`, and
// `.c` for a sibling of the path before it) to the row.
func fieldRows(doc string) map[string]string {
	rows := map[string]string{}
	paths := regexp.MustCompile("`(e\\.[^`]+|\\.[a-z_]+)`")
	for line := range strings.Lines(doc) {
		if !strings.HasPrefix(line, "| `e.") {
			continue
		}
		first, _, _ := strings.Cut(line[1:], " | ")
		last := ""
		for _, m := range paths.FindAllStringSubmatch(first, -1) {
			path := m[1]
			if strings.HasPrefix(path, ".") && last != "" {
				path = last[:strings.LastIndex(last, ".")] + path
			}
			rows[path] = line
			last = path
		}
	}
	return rows
}
