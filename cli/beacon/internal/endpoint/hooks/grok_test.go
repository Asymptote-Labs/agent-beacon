package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGrokHooksWritesManagedHookFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.json")
	if err := installGrokHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installGrokHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Grok hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"Beacon managed Grok endpoint telemetry hooks",
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUseFailure",
		"BEACON_ENDPOINT_MODE=1",
		"--platform grok",
		"BEACON_ENDPOINT_LOG='/tmp/runtime.jsonl'",
		"BEACON_ENDPOINT_CONFIG='/tmp/config.json'",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Grok hook file missing %q:\n%s", want, text)
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

	managed := filepath.Join(dir, "beacon.json")
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
	if got, want := mustGrokHooksPath(t, LevelUser), filepath.Join(home, ".grok", "hooks", "beacon.json"); got != want {
		t.Fatalf("user Grok hook path = %q, want %q", got, want)
	}

	project := t.TempDir()
	t.Chdir(project)
	if got, want := mustGrokHooksPath(t, LevelProject), filepath.Join(project, ".grok", "hooks", "beacon.json"); got != want {
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
	path := filepath.Join(t.TempDir(), "beacon.json")
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

func mustGrokHooksPath(t *testing.T, level Level) string {
	t.Helper()
	path, err := grokHooksPath(level)
	if err != nil {
		t.Fatalf("grokHooksPath returned error: %v", err)
	}
	return path
}
