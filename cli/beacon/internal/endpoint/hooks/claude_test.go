package hooks

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestClaudeSettingsPathUserLevelFollowsClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	got, err := claudeSettingsPath(LevelUser)
	if err != nil {
		t.Fatalf("claudeSettingsPath returned error: %v", err)
	}
	if want := filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Fatalf("default user settings path = %q, want %q", got, want)
	}

	config := filepath.Join(t.TempDir(), "claude-alt")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	got, err = claudeSettingsPath(LevelUser)
	if err != nil {
		t.Fatalf("claudeSettingsPath returned error: %v", err)
	}
	if want := filepath.Join(config, "settings.json"); got != want {
		t.Fatalf("user settings path with CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}

	cwd := t.TempDir()
	t.Chdir(cwd)
	got, err = claudeSettingsPath(LevelProject)
	if err != nil {
		t.Fatalf("claudeSettingsPath returned error: %v", err)
	}
	if want := filepath.Join(cwd, ".claude", "settings.json"); got != want {
		t.Fatalf("project settings path = %q, want %q (CLAUDE_CONFIG_DIR must not move it)", got, want)
	}
}
