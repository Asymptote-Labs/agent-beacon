package threatrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// specRule is a minimal valid single-event rule with the given id and spec line.
func specRule(id, specLine, extra string) string {
	return "id: " + id + "\n" + specLine + `
version: 1
title: T
severity: low
status: experimental
posture: detect
match: 'e.event.action == "file.read"'
emit:
  reason: ok
` + extra + `tests:
  - name: p
    verdict: match
    events:
      - event: { action: file.read }
`
}

func TestSupportedSpecMatchesVersionFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(specDir(t), "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != SupportedSpec {
		t.Fatalf("spec/threat-rules/VERSION is %q but the engine's SupportedSpec is %q", got, SupportedSpec)
	}
}

func TestDecodeRuleAcceptsSupportedSpecs(t *testing.T) {
	for _, line := range []string{"", "spec: threat-rules/v1", "spec: threat-rules/v1.0", "spec: " + SupportedSpec} {
		rule, err := DecodeRule([]byte(specRule("r", line, "")))
		if err != nil {
			t.Errorf("%q: decode: %v", line, err)
			continue
		}
		if err := rule.Validate(); err != nil {
			t.Errorf("%q: validate: %v", line, err)
		}
	}
}

func TestDecodeRuleRejectsNewerSpecClearly(t *testing.T) {
	for _, spec := range []string{"threat-rules/v1.3", "threat-rules/v2"} {
		// A future rule also carries the fields its version added; the spec check must
		// come first so the error names the version rather than the unknown field.
		_, err := DecodeRule([]byte(specRule("future-rule", "spec: "+spec, "future_field: true\n")))
		u, ok := AsUnsupportedSpec(err)
		if !ok {
			t.Fatalf("%s: want *UnsupportedSpecError, got %v", spec, err)
		}
		if u.RuleID != "future-rule" || u.Required != spec {
			t.Errorf("%s: got %+v", spec, u)
		}
	}
}

func TestDecodeRuleRejectsMalformedSpec(t *testing.T) {
	for _, spec := range []string{"v1.1", "threat-rules/1.1", "threat-rules/v1.1-beta"} {
		_, err := DecodeRule([]byte(specRule("r", "spec: "+spec, "")))
		if err == nil {
			t.Errorf("%q: want error", spec)
			continue
		}
		if _, ok := AsUnsupportedSpec(err); ok {
			t.Errorf("%q: malformed spec must not read as merely unsupported", spec)
		}
	}
}

func TestOrderRequiresSpecV1_1(t *testing.T) {
	c := compileCorrelation(t, OrderAny, "60s",
		CorrelationStep{ID: "a", Match: `e.event.action == "file.read"`},
		CorrelationStep{ID: "b", Match: `e.event.action == "command.executed"`},
	)
	rule := *c.Rule()
	for _, spec := range []string{"", "threat-rules/v1"} {
		rule.Spec = spec
		err := rule.Validate()
		if err == nil || !strings.Contains(err.Error(), "correlation.order requires spec: threat-rules/v1.1") {
			t.Errorf("spec %q: want the order/spec error, got %v", spec, err)
		}
	}
}

func TestParsedCommandsRequiresSpecV1_2(t *testing.T) {
	const uses = "match: 'e.command.parsed_commands.exists(c, c.name == \"rm\")'\n"
	for _, spec := range []string{"", "spec: threat-rules/v1", "spec: threat-rules/v1.1"} {
		rule, err := DecodeRule([]byte(strings.Replace(specRule("r", spec, ""), "match: 'e.event.action == \"file.read\"'\n", uses, 1)))
		if err != nil {
			t.Fatalf("%q: decode: %v", spec, err)
		}
		if err := rule.Validate(); err == nil || !strings.Contains(err.Error(), "e.command.parsed_commands requires spec: threat-rules/v1.2") {
			t.Errorf("%q: want the parsed_commands/spec error, got %v", spec, err)
		}
	}
	// Naming the field in a string is not using it.
	rule, err := DecodeRule([]byte(strings.Replace(specRule("r", "", ""), `e.event.action == "file.read"`, `e.command.command != "parsed_commands"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rule.Validate(); err != nil {
		t.Errorf("a string literal required the spec: %v", err)
	}
	rule, err = DecodeRule([]byte(strings.Replace(specRule("r", "spec: threat-rules/v1.2", ""), "match: 'e.event.action == \"file.read\"'\n", uses, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rule.Validate(); err != nil {
		t.Errorf("v1.2: %v", err)
	}
}

func TestLoadDirSkippingSetsNewerSpecAside(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("current.rule.yaml", specRule("current-rule", "spec: "+SupportedSpec, ""))
	write("legacy.rule.yaml", specRule("legacy-rule", "", ""))
	write("future.rule.yaml", specRule("future-rule", "spec: threat-rules/v1.9", "future_field: 1\n"))

	rules, skipped, err := LoadDirSkipping(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rules) != 2 || rules[0].ID != "current-rule" || rules[1].ID != "legacy-rule" {
		t.Fatalf("want current-rule and legacy-rule loaded, got %v", ruleIDs(rules))
	}
	if len(skipped) != 1 || skipped[0].Err.RuleID != "future-rule" || filepath.Base(skipped[0].Path) != "future.rule.yaml" {
		t.Fatalf("want future-rule skipped, got %+v", skipped)
	}
	if !strings.Contains(skipped[0].String(), "future.rule.yaml: rule \"future-rule\" requires threat-rules/v1.9") {
		t.Errorf("skip message should name the file, rule, and version: %s", skipped[0])
	}

	// Strict LoadDir refuses instead, naming the same rule.
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "upgrade Beacon") {
		t.Fatalf("LoadDir want the unsupported-spec error, got %v", err)
	}
	// Other failures still fail the whole skipping load.
	write("broken.rule.yaml", specRule("broken-rule", "", "typo_field: 1\n"))
	if _, _, err := LoadDirSkipping(dir); err == nil {
		t.Fatal("an unknown field in a current-spec rule must still fail the load")
	}
}

func ruleIDs(rules []*Rule) []string {
	ids := make([]string, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	return ids
}
