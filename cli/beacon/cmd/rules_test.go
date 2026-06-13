package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const validRuleYAML = `
id: cli-test-rule
version: 1
title: CLI test rule
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

func writeRuleFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newCmd() (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(buf)
	c.SetErr(buf)
	return c, buf
}

func TestRulesLintSuccess(t *testing.T) {
	dir := t.TempDir()
	writeRuleFile(t, dir, "ok.rule.yaml", validRuleYAML)
	cmd, buf := newCmd()
	if err := lintRulesPath(cmd, dir); err != nil {
		t.Fatalf("lint returned error: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "ok   cli-test-rule") {
		t.Fatalf("expected ok line, got: %s", buf.String())
	}
}

func TestRulesLintFailsOnFixtureMismatch(t *testing.T) {
	dir := t.TempDir()
	bad := strings.Replace(validRuleYAML, "action: file.read }\n", "action: tool.invoked }\n", 1)
	writeRuleFile(t, dir, "bad.rule.yaml", bad)
	cmd, buf := newCmd()
	if err := lintRulesPath(cmd, dir); err == nil {
		t.Fatalf("expected lint failure; output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Fatalf("expected FAIL line, got: %s", buf.String())
	}
}

func TestRulesLintEmptyDir(t *testing.T) {
	cmd, _ := newCmd()
	if err := lintRulesPath(cmd, t.TempDir()); err == nil {
		t.Fatalf("expected error for directory with no rule files")
	}
}

func TestLoadRuleFilesSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := writeRuleFile(t, dir, "ok.rule.yaml", validRuleYAML)
	rules, err := loadRuleFiles(p)
	if err != nil {
		t.Fatalf("loadRuleFiles single: %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "cli-test-rule" {
		t.Fatalf("unexpected: %+v", rules)
	}
}
