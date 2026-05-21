package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGrokHooksWritesManagedHookFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon-endpoint.json")
	if err := installGrokHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installGrokHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Grok hooks: %v", err)
	}
	var hooks grokHooksFile
	if err := json.Unmarshal(data, &hooks); err != nil {
		t.Fatalf("decode Grok hooks: %v", err)
	}
	if hooks.Beacon != grokManagedHookMarker {
		t.Fatalf("managed marker = %q, want %q", hooks.Beacon, grokManagedHookMarker)
	}
	wantCommands := map[string]string{
		"SessionStart":       "session-start",
		"UserPromptSubmit":   "prompt-submit",
		"PreToolUse":         "pre-tool",
		"PostToolUse":        "post-tool",
		"PostToolUseFailure": "post-tool",
		"Stop":               "stop",
		"SessionEnd":         "session-end",
	}
	for eventName, commandName := range wantCommands {
		groups := hooks.Hooks[eventName]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hook shape = %#v, want one command hook", eventName, groups)
		}
		command := groups[0].Hooks[0].Command
		for _, want := range []string{
			"BEACON_ENDPOINT_MODE=1",
			"--platform grok",
			"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
			"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
			commandName,
		} {
			if !strings.Contains(command, want) {
				t.Fatalf("%s command missing %q:\n%s", eventName, want, command)
			}
		}
	}
}

func TestRemoveGrokHooksOnlyRemovesManagedHookFile(t *testing.T) {
	dir := t.TempDir()
	userHook := filepath.Join(dir, "user.json")
	if err := os.WriteFile(userHook, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`), 0644); err != nil {
		t.Fatalf("write user hook: %v", err)
	}
	changed, err := removeGrokHooks(userHook)
	if err != nil {
		t.Fatalf("removeGrokHooks returned error: %v", err)
	}
	if changed {
		t.Fatal("user hook should not be removed")
	}
	if _, err := os.Stat(userHook); err != nil {
		t.Fatalf("user hook was removed: %v", err)
	}

	managed := filepath.Join(dir, "beacon-endpoint.json")
	if err := installGrokHooks(managed, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installGrokHooks returned error: %v", err)
	}
	changed, err = removeGrokHooks(managed)
	if err != nil {
		t.Fatalf("removeGrokHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected managed hook removal")
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("managed hook still exists or unexpected error: %v", err)
	}
}

func TestGrokHookPathLevels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := mustGrokHooksPath(t, LevelUser), filepath.Join(home, ".grok", "hooks", "beacon-endpoint.json"); got != want {
		t.Fatalf("user Grok hook path = %q, want %q", got, want)
	}

	project := t.TempDir()
	t.Chdir(project)
	if got, want := mustGrokHooksPath(t, LevelProject), filepath.Join(project, ".grok", "hooks", "beacon-endpoint.json"); got != want {
		t.Fatalf("project Grok hook path = %q, want %q", got, want)
	}
}

func TestGrokProjectStatusMentionsTrust(t *testing.T) {
	status := grokProjectTrustMessage(GrokStatus{Message: "Grok endpoint hooks installed"}, LevelProject)
	if !strings.Contains(status.Message, "/hooks-trust") {
		t.Fatalf("project status message = %q, want /hooks-trust guidance", status.Message)
	}
}

func TestGrokInstalledUsesManagedEndpointCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon-endpoint.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 echo keep"}]}]}}`), 0644); err != nil {
		t.Fatalf("write unmarked hook: %v", err)
	}
	if isGrokInstalledAt(path) {
		t.Fatal("unmanaged hook should not be detected as installed")
	}
	if err := installGrokHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installGrokHooks returned error: %v", err)
	}
	if !isGrokInstalledAt(path) {
		t.Fatal("managed Grok hook should be detected")
	}
}

func TestGrokManagedDetectionRequiresMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon-endpoint.json")
	if err := os.WriteFile(path, []byte(`{"description":"user copied Beacon command","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform grok session-start"}]}]}}`), 0644); err != nil {
		t.Fatalf("write unmarked copied command hook: %v", err)
	}
	if isGrokInstalledAt(path) {
		t.Fatal("unmarked copied Beacon command should not be detected as managed")
	}
	changed, err := removeGrokHooks(path)
	if err != nil {
		t.Fatalf("removeGrokHooks returned error: %v", err)
	}
	if changed {
		t.Fatal("unmarked copied Beacon command should not be removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unmarked hook was removed: %v", err)
	}
}

func mustGrokHooksPath(t *testing.T, level Level) string {
	t.Helper()
	path, err := grokHooksPath(level)
	if err != nil {
		t.Fatalf("grokHooksPath returned error: %v", err)
	}
	return path
}
