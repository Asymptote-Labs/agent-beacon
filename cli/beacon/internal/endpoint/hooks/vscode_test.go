package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallVSCodeHooksPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.json")
	existing := `{"hooks":{"SessionStart":[{"type":"command","command":"echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installVSCodeHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installVSCodeHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"echo keep",
		"BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"'/tmp/beacon hooks' --platform vscode",
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"SubagentStart",
		"SubagentStop",
		"Stop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("hooks missing %q:\n%s", want, text)
		}
	}
}

func TestRemoveVSCodeEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.json")
	existing := `{"hooks":{"PreToolUse":[{"type":"command","command":"echo keep"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform vscode pre-tool"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeVSCodeEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeVSCodeEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestVSCodeHooksPathLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userPath, err := vscodeHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("user path error: %v", err)
	}
	if want := filepath.Join(home, ".copilot", "hooks", "beacon.json"); userPath != want {
		t.Fatalf("user path = %q, want %q", userPath, want)
	}

	project := t.TempDir()
	t.Chdir(project)
	projectPath, err := vscodeHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("project path error: %v", err)
	}
	if want := filepath.Join(project, ".github", "hooks", "beacon.json"); projectPath != want {
		t.Fatalf("project path = %q, want %q", projectPath, want)
	}
}
