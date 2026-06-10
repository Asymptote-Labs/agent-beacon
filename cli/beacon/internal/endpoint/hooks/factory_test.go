package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallFactorySettingsPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"enabledPlugins":{"core@factory-plugins":true},"logoAnimation":"off","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installFactorySettings(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installFactorySettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"enabledPlugins",
		"logoAnimation",
		"echo keep",
		"BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"'/tmp/beacon hooks' --platform factory",
		"SessionStart",
		"UserPromptSubmit",
		"prompt-submit",
		"PostToolUse",
		"Write|Edit|MultiEdit|Create",
		"Stop",
		"SessionEnd",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing %q:\n%s", want, text)
		}
	}
}

func TestInstallFactorySettingsReplacesOldBeaconAndAsymptoteHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform factory post-tool"}]},{"hooks":[{"type":"command","command":"/tmp/asym-hooks --platform factory post-tool"}]},{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installFactorySettings(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installFactorySettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old-beacon-hooks") || strings.Contains(text, "asym-hooks") {
		t.Fatalf("old endpoint hook was not replaced:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "/tmp/new-beacon-hooks") {
		t.Fatalf("expected preserved hook and new hook:\n%s", text)
	}
}

func TestInstallFactorySettingsPreservesUserHookInMixedGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PostToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform factory post-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installFactorySettings(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installFactorySettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old-beacon-hooks") {
		t.Fatalf("old endpoint hook was not replaced:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("user hook in mixed group was not preserved:\n%s", text)
	}
	if !strings.Contains(text, "/tmp/new-beacon-hooks") {
		t.Fatalf("new endpoint hook was not installed:\n%s", text)
	}
}

func TestRemoveFactoryEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory session-start"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeFactoryEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeFactoryEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestRemoveFactoryEndpointHooksPreservesUserHookInMixedGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"PostToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory post-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeFactoryEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeFactoryEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("user hook in mixed group was not preserved:\n%s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestReadFactorySettingsReturnsCorruptJSONError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt settings: %v", err)
	}

	if _, err := readFactorySettings(path); err == nil {
		t.Fatal("expected corrupt settings error")
	}
}

func TestInstallFactorySettingsHandlesNullHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":null}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installFactorySettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installFactorySettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), "UserPromptSubmit") {
		t.Fatalf("Factory hooks were not installed from hooks:null:\n%s", string(data))
	}
}

func TestInstallFactorySettingsDoesNotReplaceUserCommandWithBeaconEnvOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installFactorySettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installFactorySettings returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), "BEACON_ENDPOINT_MODE=1 echo keep") {
		t.Fatalf("user hook with Beacon-like env token was removed:\n%s", string(data))
	}
}

func TestFactorySettingsPathProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := factorySettingsPath(LevelProject)
	if err != nil {
		t.Fatalf("factorySettingsPath returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".factory", "settings.json"); got != want {
		t.Fatalf("project settings path = %q, want %q", got, want)
	}
}

func TestFactoryHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".factory", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := FactoryHookStatus(FactoryOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("FactoryHookStatus installed = false, status=%#v", status)
	}
	if status.SettingsPath != path {
		t.Fatalf("SettingsPath = %q, want %q", status.SettingsPath, path)
	}
}

func TestInstallFactoryUsesSystemConfigForSystemLog(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("embedded hooks binary is darwin-only; skipping on", runtime.GOOS)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	status, err := InstallFactory(FactoryOptions{
		Level:    LevelUser,
		LogPath:  "/var/log/beacon-agent/runtime.jsonl",
		UserMode: true,
	})
	if err != nil {
		t.Fatalf("InstallFactory returned error: %v", err)
	}
	data, err := os.ReadFile(status.SettingsPath)
	if err != nil {
		t.Fatalf("read Factory settings: %v", err)
	}
	if !strings.Contains(string(data), "BEACON_ENDPOINT_CONFIG='/Library/Application Support/Beacon/Endpoint/config.json'") {
		t.Fatalf("Factory hook did not use system config for system log:\n%s", string(data))
	}
}
