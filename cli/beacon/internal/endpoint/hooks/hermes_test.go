package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallHermesConfigPreservesExistingSettingsAndHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	existing := `model: anthropic/claude-sonnet-4.6
hooks_auto_accept: true
hooks:
  pre_tool_call:
    - matcher: terminal
      command: ~/.hermes/agent-hooks/user-policy.sh
      timeout: 5
`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installHermesConfig(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installHermesConfig returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"model:",
		"anthropic/claude-sonnet-4.6",
		"hooks_auto_accept: true",
		"~/.hermes/agent-hooks/user-policy.sh",
		"env BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
		"'/tmp/beacon hooks' --platform hermes",
		"on_session_start:",
		"pre_llm_call:",
		"pre_tool_call:",
		"post_tool_call:",
		"pre_approval_request:",
		"post_approval_response:",
		"subagent_stop:",
		"on_session_end:",
		"on_session_finalize:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Hermes config missing %q:\n%s", want, text)
		}
	}
}

func TestInstallHermesConfigReplacesOnlyHermesBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	existing := `hooks:
  post_tool_call:
    - command: env BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform hermes post-tool
    - command: /tmp/asym-hooks --platform hermes post-tool
    - command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory post-tool
    - command: echo keep
`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installHermesConfig(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installHermesConfig returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old-beacon-hooks") || strings.Contains(text, "asym-hooks") {
		t.Fatalf("old Hermes endpoint hook was not replaced:\n%s", text)
	}
	for _, want := range []string{"--platform factory", "echo keep", "/tmp/new-beacon-hooks"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected preserved hooks and new hook %q:\n%s", want, text)
		}
	}
}

func TestRemoveHermesEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	existing := `hooks:
  pre_tool_call:
    - matcher: terminal
      command: echo keep
    - command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform hermes pre-tool
    - command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform factory pre-tool
`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeHermesEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeHermesEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "--platform hermes") {
		t.Fatalf("Hermes endpoint hook was not removed:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "--platform factory") {
		t.Fatalf("non-Hermes hooks were not preserved:\n%s", text)
	}
}

func TestHermesHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".hermes", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`hooks:
  on_session_end:
    - command: env BEACON_ENDPOINT_MODE=1 beacon-hooks --platform hermes session-end
`), 0600); err != nil {
		t.Fatal(err)
	}

	status := HermesHookStatus(HermesOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("HermesHookStatus installed = false, status=%#v", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}

func TestHermesProjectLevelReturnsClearError(t *testing.T) {
	if _, err := hermesConfigPath(LevelProject); err == nil || !strings.Contains(err.Error(), "user-level config only") {
		t.Fatalf("hermesConfigPath project err = %v, want user-level config only", err)
	}
}
