package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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
		"--log '/tmp/runtime.jsonl'",
		"--config '/tmp/config.json'",
		"'/tmp/beacon hooks' --platform antigravity",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Antigravity hooks missing %q:\n%s", want, text)
		}
	}
	var blocks map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		t.Fatal(err)
	}
	managed := blocks["beacon-endpoint"]
	if len(managed) != 5 {
		t.Errorf("managed hook events = %v, want five lifecycle and tool events", managed)
	}
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	for event, suffix := range map[string]string{"SessionStart": " session-start", "PreInvocation": " prompt-submit", "Stop": " stop"} {
		var handlers []handler
		if err := json.Unmarshal(managed[event], &handlers); err != nil {
			t.Errorf("%s must be a direct handler list: %v", event, err)
			continue
		}
		if len(handlers) != 1 || handlers[0].Type != "command" || !strings.HasSuffix(handlers[0].Command, suffix) {
			t.Errorf("%s handlers = %#v, want one command ending in %q", event, handlers, suffix)
		}
	}
	for event, suffix := range map[string]string{"PreToolUse": " pre-tool", "PostToolUse": " post-tool"} {
		var groups []struct {
			Matcher string    `json:"matcher"`
			Hooks   []handler `json:"hooks"`
		}
		if err := json.Unmarshal(managed[event], &groups); err != nil {
			t.Errorf("%s must be a matcher group list: %v", event, err)
			continue
		}
		if len(groups) != 1 || groups[0].Matcher != "*" || len(groups[0].Hooks) != 1 ||
			groups[0].Hooks[0].Type != "command" || !strings.HasSuffix(groups[0].Hooks[0].Command, suffix) {
			t.Errorf("%s groups = %#v, want a wildcard group with one command ending in %q", event, groups, suffix)
		}
	}
}

func TestInstallAntigravityHooksReplacesManagedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform antigravity stop"}]}]},"other":{"Stop":[{"hooks":[{"command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if !isAntigravityInstalledAt(path) {
		t.Error("managed legacy grouped Stop must remain eligible for repair")
	}
	if err := installAntigravityHooks(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installAntigravityHooks returned error: %v", err)
	}
	if !isAntigravityInstalledAt(path) {
		t.Error("refreshed hooks were not detected")
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
	testenv.SetHome(t, home)
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
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"beacon-endpoint":{"Stop":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := AntigravityHookStatus(AntigravityOptions{Level: LevelUser, UserMode: true})
	if !status.Installed || status.NeedsReinstall {
		t.Fatalf("AntigravityHookStatus = %#v, want installed in the current schema", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}

// A managed block in the legacy grouped schema stays installed, so repair rewrites it, but
// Antigravity never runs it, so status must say so and name the command that rewrites it.
func TestAntigravityHookStatusReportsLegacySchema(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := AntigravityHookStatus(AntigravityOptions{Level: LevelUser, UserMode: true})
	if !status.Installed || !status.NeedsReinstall {
		t.Fatalf("AntigravityHookStatus = %#v, want installed and needing a reinstall", status)
	}
	const want = "Beacon's Antigravity CLI hooks use the legacy schema, which Antigravity does not run; rerun beacon endpoint hooks install --harness antigravity to rewrite them"
	if status.Message != want {
		t.Fatalf("Message = %q, want %q", status.Message, want)
	}
	if got := AntigravityLegacyHooksMessage(LevelProject); !strings.Contains(got, "--harness antigravity --level project ") {
		t.Fatalf("project-level message = %q, want the project-level install command", got)
	}
}

func TestAntigravityConfigEndpointHooks(t *testing.T) {
	const command = `"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"`
	for _, tc := range []struct {
		name                    string
		config                  string
		wantCurrent, wantLegacy bool
	}{
		{"legacy", `{"beacon-endpoint":{"Stop":[{"hooks":[{` + command + `}]}],"PreToolUse":[{"hooks":[{` + command + `}]}]}}`, false, true},
		{"unsupported event", `{"beacon-endpoint":{"UserPromptSubmit":[{` + command + `}]}}`, false, false},
		{"custom block", `{"custom":{"Stop":[{` + command + `}]}}`, true, false},
		{"custom block with non-command handler", `{"custom":{"Stop":[{"type":"other"},{` + command + `}]}}`, true, false},
		{"someone else's legacy block", `{"custom":{"Stop":[{"hooks":[{"command":"echo keep"}]}]}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, legacy, err := AntigravityConfigEndpointHooks([]byte(tc.config))
			if err != nil || current != tc.wantCurrent || legacy != tc.wantLegacy {
				t.Fatalf("current = %v, legacy = %v, err = %v, want %v, %v", current, legacy, err, tc.wantCurrent, tc.wantLegacy)
			}
		})
	}
}
