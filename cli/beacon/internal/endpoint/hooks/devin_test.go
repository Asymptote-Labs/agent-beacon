package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDevinProjectHooksPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"PreToolUse":[{"matcher":"exec","hooks":[{"type":"command","command":"echo keep"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"echo keep",
		"BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"'/tmp/beacon hooks' --platform devin",
		"PermissionRequest",
		"permission-request",
		"PreToolUse",
		"PostToolUse",
		"SessionStart",
		"SessionEnd",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Devin hooks missing %q:\n%s", want, text)
		}
	}
}

func TestInstallDevinUserConfigPreservesUnrelatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	existing := `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"theme"`) || !strings.Contains(text, "echo keep") || !strings.Contains(text, "--platform devin") {
		t.Fatalf("user config was not preserved and updated:\n%s", text)
	}
}

func TestInstallDevinHooksReplacesOldBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"PostToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform devin post-tool"}]},{"hooks":[{"type":"command","command":"echo keep"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinHooks(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinHooks returned error: %v", err)
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
		t.Fatalf("expected preserved hook and new hook:\n%s", text)
	}
}

func TestRemoveDevinEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin session-start"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeDevinEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeDevinEndpointHooks returned error: %v", err)
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
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestReadDevinConfigReturnsCorruptJSONError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if _, err := readDevinConfig(path); err == nil {
		t.Fatal("expected corrupt config error")
	}
}

func TestDevinConfigPathProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := devinConfigPath(LevelProject)
	if err != nil {
		t.Fatalf("devinConfigPath returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".devin", "hooks.v1.json"); got != want {
		t.Fatalf("project config path = %q, want %q", got, want)
	}
}

func TestDevinHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "devin", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := DevinHookStatus(DevinOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("DevinHookStatus installed = false, status=%#v", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}
