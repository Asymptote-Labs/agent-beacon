package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestInstallCodexHooksWritesSessionContextAndAssistantResponse(t *testing.T) {
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
		"codex-assistant-response",
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
	if !strings.Contains(text, "SessionStart") {
		t.Fatalf("existing non-Beacon SessionStart should remain:\n%s", text)
	}
	// The old Beacon Stop entry is replaced, not kept beside the new one: Stop holds exactly the
	// assistant-response command.
	var parsed struct {
		Hooks map[string][]settingsHookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("decode hooks: %v", err)
	}
	var stopCommands []string
	for _, group := range parsed.Hooks["Stop"] {
		for _, hook := range group.Hooks {
			stopCommands = append(stopCommands, hook.Command)
		}
	}
	if len(stopCommands) != 1 || !strings.HasSuffix(stopCommands[0], " codex-assistant-response") {
		t.Fatalf("Stop commands = %q, want only codex-assistant-response", stopCommands)
	}
}

func TestInstallCodexHooksIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	for i := 0; i < 2; i++ {
		if err := installCodexHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "codex-assistant-response"); n != 1 {
		t.Fatalf("codex-assistant-response appears %d times after two installs, want 1:\n%s", n, data)
	}
	removed, err := removeCodexEndpointHooks(path)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "codex-assistant-response") || strings.Contains(string(data), "codex-session-context") {
		t.Fatalf("uninstall left Beacon hooks behind:\n%s", data)
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
