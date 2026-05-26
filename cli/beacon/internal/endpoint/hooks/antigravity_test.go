package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAntigravityHooksPreservesNonBeaconBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"my-linter-hook":{"PostToolUse":[{"matcher":"run_command","hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installAntigravityHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installAntigravityHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"my-linter-hook",
		"echo keep",
		"beacon-endpoint",
		"BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"'/tmp/beacon hooks' --platform antigravity",
		"PreInvocation",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"PostInvocation",
		"Stop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Antigravity hooks missing %q:\n%s", want, text)
		}
	}
}

func TestInstallAntigravityHooksReplacesManagedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform antigravity stop"}]}]},"other":{"Stop":[{"hooks":[{"command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installAntigravityHooks(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installAntigravityHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old-beacon-hooks") {
		t.Fatalf("old endpoint hook was not replaced:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "/tmp/new-beacon-hooks") {
		t.Fatalf("expected preserved block and new hook:\n%s", text)
	}
}

func TestRemoveAntigravityEndpointHooksPreservesOtherBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}]},"other":{"Stop":[{"hooks":[{"command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeAntigravityEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeAntigravityEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon block was not preserved:\n%s", text)
	}
	if strings.Contains(text, "beacon-endpoint") || strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestReadAntigravityConfigReturnsCorruptJSONError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if _, err := readAntigravityConfig(path); err == nil {
		t.Fatal("expected corrupt config error")
	}
}

func TestAntigravityConfigPathLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Chdir(dir)

	userPath, err := antigravityConfigPath(LevelUser)
	if err != nil {
		t.Fatalf("user antigravityConfigPath returned error: %v", err)
	}
	if got, want := userPath, filepath.Join(home, ".gemini", "config", "hooks.json"); got != want {
		t.Fatalf("user config path = %q, want %q", got, want)
	}
	projectPath, err := antigravityConfigPath(LevelProject)
	if err != nil {
		t.Fatalf("project antigravityConfigPath returned error: %v", err)
	}
	if got, want := projectPath, filepath.Join(dir, ".agents", "hooks.json"); got != want {
		t.Fatalf("project config path = %q, want %q", got, want)
	}
}

func TestAntigravityHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := AntigravityHookStatus(AntigravityOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("AntigravityHookStatus installed = false, status=%#v", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}
