package hooks

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallClaudeSettingsPreservesExistingSettingsAndHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"env":{"EXISTING":"1"},"permissions":{"defaultMode":"default"},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory post-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeSettings(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClaudeSettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"EXISTING",
		"defaultMode",
		"echo keep",
		"--platform factory",
		"--log '/tmp/runtime.jsonl'",
		"--config '/tmp/config.json'",
		"'/tmp/beacon hooks' --platform claude",
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"Bash|Edit|Write|MultiEdit|Read|Glob|Grep|WebFetch|WebSearch|Agent|mcp__.*",
		"PostToolUseFailure",
		"SubagentStart",
		"SubagentStop",
		"PermissionRequest",
		"SessionEnd",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing %q:\n%s", want, text)
		}
	}
}

func TestInstallClaudeSettingsReplacesOnlyClaudeBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform claude post-tool"}]},{"hooks":[{"type":"command","command":"/tmp/asym-hooks --platform claude post-tool"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory post-tool"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks post-tool"}]},{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeSettings(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClaudeSettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old-beacon-hooks") || strings.Contains(text, "asym-hooks") {
		t.Fatalf("old Claude endpoint hook was not replaced:\n%s", text)
	}
	if !strings.Contains(text, "--platform factory") || !strings.Contains(text, "beacon-hooks post-tool") || !strings.Contains(text, "echo keep") || !strings.Contains(text, "/tmp/new-beacon-hooks") {
		t.Fatalf("expected preserved hooks and new hook:\n%s", text)
	}
}

func TestRemoveClaudeEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform claude session-start"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory session-start"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeClaudeEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeClaudeEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "--platform claude") {
		t.Fatalf("Claude endpoint hook was not removed:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "--platform factory") {
		t.Fatalf("non-Claude hooks were not preserved:\n%s", text)
	}
}

func TestInstallClaudeSettingsHandlesNullHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":null}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeSettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClaudeSettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), "UserPromptSubmit") {
		t.Fatalf("Claude hooks were not installed from hooks:null:\n%s", string(data))
	}
}

func TestClaudeSettingsPathProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := claudeSettingsPath(LevelProject)
	if err != nil {
		t.Fatalf("claudeSettingsPath returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".claude", "settings.json"); got != want {
		t.Fatalf("project settings path = %q, want %q", got, want)
	}
}

func TestClaudeHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform claude stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := ClaudeHookStatus(ClaudeOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("ClaudeHookStatus installed = false, status=%#v", status)
	}
	if status.SettingsPath != path {
		t.Fatalf("SettingsPath = %q, want %q", status.SettingsPath, path)
	}
}
