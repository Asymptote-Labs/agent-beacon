package threatrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleRule = `
id: %s
version: 1
title: Sample
severity: low
status: experimental
posture: detect
match: 'e.event.action == "file.read"'
emit:
  reason: ok
tests:
  - name: p
    verdict: match
    events:
      - event: { action: file.read }
`

func writeRule(t *testing.T, dir, name, contents string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	writeRule(t, filepath.Join(root, "a"), "one.rule.yaml", strings.Replace(sampleRule, "%s", "rule-one", 1))
	writeRule(t, filepath.Join(root, "b"), "two.rule.yaml", strings.Replace(sampleRule, "%s", "rule-two", 1))
	// A non-rule file must be ignored.
	writeRule(t, root, "notes.txt", "ignore me")

	rules, err := LoadDir(root)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	if rules[0].ID != "rule-one" || rules[1].ID != "rule-two" {
		t.Fatalf("rules not sorted by path/id: %s, %s", rules[0].ID, rules[1].ID)
	}
}

func TestLoadDirIgnoresAppleDoubleRuleFiles(t *testing.T) {
	root := t.TempDir()
	writeRule(t, root, "ok.rule.yaml", strings.Replace(sampleRule, "%s", "rule-one", 1))
	writeRule(t, root, "._ok.rule.yaml", "\x00\x05metadata")
	rules, err := LoadDir(root)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "rule-one" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

func TestLoadDirDuplicateID(t *testing.T) {
	root := t.TempDir()
	writeRule(t, filepath.Join(root, "a"), "x.rule.yaml", strings.Replace(sampleRule, "%s", "dup", 1))
	writeRule(t, filepath.Join(root, "b"), "y.rule.yaml", strings.Replace(sampleRule, "%s", "dup", 1))
	_, err := LoadDir(root)
	if err == nil || !strings.Contains(err.Error(), "duplicate rule id") {
		t.Fatalf("expected duplicate-id error, got %v", err)
	}
}

func TestLoadDirMalformedNamesFile(t *testing.T) {
	root := t.TempDir()
	path := writeRule(t, root, "bad.rule.yaml", "id: [not-a-string\n")
	_, err := LoadDir(root)
	if err == nil {
		t.Fatalf("expected error for malformed file")
	}
	if !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Fatalf("error %q should name the offending file %q", err.Error(), filepath.Base(path))
	}
}

func TestLoadDirRejectsInvalidRule(t *testing.T) {
	root := t.TempDir()
	// Valid YAML, invalid rule (bad CEL field).
	bad := strings.Replace(sampleRule, "%s", "bad-cel", 1)
	bad = strings.Replace(bad, `e.event.action == "file.read"`, `e.nope.field == 1`, 1)
	writeRule(t, root, "badcel.rule.yaml", bad)
	_, err := LoadDir(root)
	if err == nil || !strings.Contains(err.Error(), "validate") {
		t.Fatalf("expected validate error, got %v", err)
	}
}
