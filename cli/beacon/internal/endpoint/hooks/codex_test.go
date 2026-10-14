package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestInstallCodexHooksWritesSessionContextOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform codex inventory-heartbeat"}]}],"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform codex codex-usage-sync"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installCodexHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installCodexHooks returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"echo keep",
		"SessionStart",
		"--platform codex",
		"codex-session-context",
		"--log '/tmp/runtime.jsonl'",
		"--config '/tmp/config.json'",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Codex hooks missing %q:\n%s", want, text)
		}
	}
	// The fixture's old inventory-heartbeat entry proves a reinstall strips what older versions wrote.
	for _, forbidden := range []string{"session-start", "prompt-submit", " stop", " session-end", "codex-usage-sync", "inventory-heartbeat", "UserPromptSubmit", "--cli"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Codex hooks should not install duplicate generic runtime hooks %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "SessionStart") || strings.Contains(text, `"Stop"`) {
		t.Fatalf("existing non-Beacon SessionStart should remain and old Beacon Stop should be removed:\n%s", text)
	}
}

func TestRemoveCodexEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform codex inventory-heartbeat"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	updated, err := removeCodexEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeCodexEndpointHooks returned error: %v", err)
	}
	if !updated {
		t.Fatal("expected Codex endpoint hook to be removed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "--platform codex") {
		t.Fatalf("Codex endpoint hook was not removed:\n%s", text)
	}
}

// Codex reads its user hooks.json from CODEX_HOME when that is set; project hooks stay in
// <cwd>/.codex, which CODEX_HOME does not move.
func TestCodexHooksPathFollowsCodexHome(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	resolved, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("codexHooksPath(user): %v", err)
	}
	if want := filepath.Join(resolved, "hooks.json"); path != want {
		t.Fatalf("codexHooksPath(user) = %q, want %q", path, want)
	}

	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err = codexHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("codexHooksPath(project): %v", err)
	}
	if want := filepath.Join(cwd, ".codex", "hooks.json"); path != want {
		t.Fatalf("codexHooksPath(project) = %q, want %q", path, want)
	}

	t.Setenv("CODEX_HOME", filepath.Join(codexHome, "missing"))
	if path, err := codexHooksPath(LevelUser); err == nil {
		t.Fatalf("codexHooksPath(user) = %q for a CODEX_HOME that does not exist, want an error", path)
	}
}
