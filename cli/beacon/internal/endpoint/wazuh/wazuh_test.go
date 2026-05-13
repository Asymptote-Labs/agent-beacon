package wazuh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalfileSnippetUsesConfiguredPath(t *testing.T) {
	got := LocalfileSnippet("/tmp/beacon/runtime.jsonl")
	if !strings.Contains(got, "/tmp/beacon/runtime.jsonl") {
		t.Fatalf("snippet did not include configured path: %s", got)
	}
	if strings.Contains(got, "{{LOG_PATH}}") {
		t.Fatalf("snippet still contains template token: %s", got)
	}
}

func TestInstallPackWritesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := InstallPack(dir, "/tmp/beacon/runtime.jsonl"); err != nil {
		t.Fatalf("InstallPack returned error: %v", err)
	}
	for _, name := range []string{"ossec-localfile.xml", "beacon-rules.xml", "sample-event.jsonl", "README.md"} {
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
	if !strings.Contains(sample, `"content":{"retention":"metadata","included":false}`) {
		t.Fatalf("sample event missing metadata retention: %s", sample)
	}
}
