package wazuh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalfileSnippetUsesConfiguredPath(t *testing.T) {
	got, err := LocalfileSnippet("/tmp/beacon/runtime.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("snippet did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("snippet still contains template token: %s", got)
	}
}

// Wazuh's XML parser reads a '<' in the location as the start of an element and has no escape for it.
func TestLogPathWithAngleBracketIsRefused(t *testing.T) {
	const path = "/tmp/a<b/runtime.jsonl"
	if got, err := LocalfileSnippet(path); err == nil {
		t.Fatalf("LocalfileSnippet accepted %q: %s", path, got)
	}
	dir := filepath.Join(t.TempDir(), "pack")
	if err := InstallPack(dir, path); err == nil {
		t.Fatalf("InstallPack accepted %q", path)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("InstallPack wrote %s before refusing the path: %v", dir, err)
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"ossec-localfile.xml", "beacon-rules.xml", "sample-event.jsonl", "apply-dashboard-default-columns.sh", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
}

func TestRulesCoverAgentWorkflowActions(t *testing.T) {
	rules := mustRead("pack/beacon-rules.xml")
	for _, action := range []string{"command.executed", "mcp.tool_invoked", "tool.failed"} {
		if !strings.Contains(rules, action) {
			t.Fatalf("rules missing action %s", action)
		}
	}
	sample := mustRead("pack/sample-event.jsonl")
	if !strings.Contains(sample, `"action":"command.executed"`) {
		t.Fatalf("sample event missing command action: %s", sample)
	}
	if strings.Contains(sample, `"content":{"retention"`) {
		t.Fatalf("sample event should not include retention marker: %s", sample)
	}
}

func TestDashboardColumnsScriptIncludesBeaconFields(t *testing.T) {
	script := mustRead("pack/apply-dashboard-default-columns.sh")
	for _, field := range []string{"data.event.action", "data.prompt.text", "data.command", "data.file", "data.session.id"} {
		if !strings.Contains(script, field) {
			t.Fatalf("dashboard columns script missing %s", field)
		}
	}
	for _, field := range []string{"data.command.command", "data.file.path", "data.tool.name"} {
		if strings.Contains(script, field) {
			t.Fatalf("dashboard columns script includes unmapped Wazuh field %s", field)
		}
	}
}
