package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type hookFileChange struct {
	path      string
	installed bool
}

func TestCursorExtraHooksFollowBeaconHooks(t *testing.T) {
	withoutHookExtensions(t)
	var changes []hookFileChange
	enabled := true
	cursorExtraHooks = func(prefix string) map[string][]HookRef {
		if !enabled {
			// Named with no entries: the events are stripped of Beacon's and get nothing new.
			return map[string][]HookRef{"beforeSubmitPrompt": nil, "beforeMCPExecution": nil}
		}
		return map[string][]HookRef{
			"beforeSubmitPrompt": {{Command: prefix + " extra-prompt", Timeout: 10}},
			"beforeMCPExecution": {{Command: prefix + " extra-tool", Timeout: 15}},
		}
	}
	cursorHooksChanged = func(path string, installed bool) error {
		changes = append(changes, hookFileChange{path, installed})
		return nil
	}

	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"beforeSubmitPrompt":[{"command":"echo keep"}],"beforeMCPExecution":[{"command":"echo mine","failClosed":true}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// Installing twice leaves one copy of each entry: the second install strips the first
	// install's, extra ones included, before writing them again.
	for i := 0; i < 2; i++ {
		if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatal(err)
		}
	}
	commands := cursorCommands(t, path)
	prompt := commands["beforeSubmitPrompt"]
	if len(prompt) != 3 || prompt[0] != "echo keep" || !strings.HasSuffix(prompt[1], " prompt-submit") || !strings.HasSuffix(prompt[2], " extra-prompt") {
		t.Fatalf("beforeSubmitPrompt = %q, want the user's, Beacon's and the extra entry in that order", prompt)
	}
	mcp := commands["beforeMCPExecution"]
	if len(mcp) != 2 || mcp[0] != "echo mine" || !strings.HasSuffix(mcp[1], " extra-tool") {
		t.Fatalf("beforeMCPExecution = %q, want the user's entry and the extra one", mcp)
	}
	if !strings.Contains(readFile(t, path), `"failClosed": true`) {
		t.Fatalf("the user's entry lost a field Beacon does not model:\n%s", readFile(t, path))
	}
	if !isCursorInstalledAt(path) {
		t.Fatal("status does not see the installed hooks")
	}

	// A build that stops installing its hooks names their events with none.
	enabled = false
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	commands = cursorCommands(t, path)
	if prompt := commands["beforeSubmitPrompt"]; len(prompt) != 2 || !strings.HasSuffix(prompt[1], " prompt-submit") {
		t.Fatalf("beforeSubmitPrompt after the extension dropped its entry = %q", prompt)
	}
	if mcp := commands["beforeMCPExecution"]; !reflect.DeepEqual(mcp, []string{"echo mine"}) {
		t.Fatalf("beforeMCPExecution after the extension dropped its entry = %q", mcp)
	}

	// An event that held only Beacon's entries goes away with them.
	enabled = true
	if err := os.WriteFile(path, []byte(`{"version":1,"hooks":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	enabled = false
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cursorCommands(t, path)["beforeMCPExecution"]; ok {
		t.Fatalf("an event left with nothing was kept:\n%s", readFile(t, path))
	}

	enabled = true
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := removeCursorEndpointHooks(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("uninstall left a file holding only Beacon's hooks behind: %v", err)
	}

	want := []hookFileChange{{path, true}, {path, true}, {path, true}, {path, true}, {path, true}, {path, true}, {path, false}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("hooks-changed calls = %+v, want %+v", changes, want)
	}
}

func TestCursorHooksChangedErrorIsReturned(t *testing.T) {
	withoutHookExtensions(t)
	cursorHooksChanged = func(string, bool) error { return os.ErrPermission }
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != os.ErrPermission {
		t.Fatalf("install error = %v, want the callback's", err)
	}
	if _, err := removeCursorEndpointHooks(path); err != os.ErrPermission {
		t.Fatalf("uninstall error = %v, want the callback's", err)
	}
}

func TestCodexExtraHookGroupsFollowBeaconHooks(t *testing.T) {
	withoutHookExtensions(t)
	var changes []hookFileChange
	enabled := true
	codexExtraHookGroups = func(prefix string) map[string][]settingsHookGroup {
		if !enabled {
			return nil
		}
		return map[string][]settingsHookGroup{
			"SessionStart":     {{Hooks: []settingsHookRef{{Type: "command", Command: prefix + " extra-session", Timeout: 15}}}},
			"UserPromptSubmit": {{Hooks: []settingsHookRef{{Type: "command", Command: prefix + " extra-prompt", Timeout: 10}}}},
			"PreToolUse":       {{Matcher: "^mcp__.*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " extra-tool", Timeout: 15}}}},
		}
	}
	codexHooksChanged = func(path string, installed bool) error {
		changes = append(changes, hookFileChange{path, installed})
		return nil
	}

	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := installCodexHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := readSettingsHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	pre := settings.hooks["PreToolUse"]
	if len(pre) != 2 || pre[0].Hooks[0].Command != "echo keep" || pre[1].Matcher != "^mcp__.*" || !strings.HasSuffix(pre[1].Hooks[0].Command, " extra-tool") || pre[1].Hooks[0].Timeout != 15 {
		t.Fatalf("PreToolUse groups = %+v, want the user's and the extra one", pre)
	}
	start := settings.hooks["SessionStart"]
	if len(start) != 2 || !strings.HasSuffix(start[0].Hooks[0].Command, " codex-session-context") || !strings.HasSuffix(start[1].Hooks[0].Command, " extra-session") {
		t.Fatalf("SessionStart groups = %+v, want Beacon's then the extra one", start)
	}
	if prompt := settings.hooks["UserPromptSubmit"]; len(prompt) != 1 || !strings.HasSuffix(prompt[0].Hooks[0].Command, " extra-prompt") {
		t.Fatalf("UserPromptSubmit groups = %+v", prompt)
	}

	// Without the extra groups a reinstall removes them.
	enabled = false
	if err := installCodexHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if strings.Contains(text, "extra-") || strings.Contains(text, "UserPromptSubmit") {
		t.Fatalf("a reinstall without the extra groups kept them:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "codex-session-context") {
		t.Fatalf("a reinstall lost the user's or Beacon's own hooks:\n%s", text)
	}

	enabled = true
	if err := installCodexHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	if !isCodexInstalledAt(path) {
		t.Fatal("status does not see the installed hooks")
	}
	if _, err := removeCodexEndpointHooks(path); err != nil {
		t.Fatal(err)
	}
	text = readFile(t, path)
	if strings.Contains(text, "beacon-hooks") || !strings.Contains(text, "echo keep") {
		t.Fatalf("uninstall left Beacon's hooks or removed the user's:\n%s", text)
	}

	want := []hookFileChange{{path, true}, {path, true}, {path, true}, {path, true}, {path, false}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("hooks-changed calls = %+v, want %+v", changes, want)
	}
}

func TestCodexHooksChangedErrorIsReturned(t *testing.T) {
	withoutHookExtensions(t)
	codexHooksChanged = func(string, bool) error { return os.ErrPermission }
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := installCodexHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != os.ErrPermission {
		t.Fatalf("install error = %v, want the callback's", err)
	}
	if _, err := removeCodexEndpointHooks(path); err != os.ErrPermission {
		t.Fatalf("uninstall error = %v, want the callback's", err)
	}
}

func cursorCommands(t *testing.T, path string) map[string][]string {
	t.Helper()
	var parsed struct {
		Hooks map[string][]HookRef `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &parsed); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]string, len(parsed.Hooks))
	for event, refs := range parsed.Hooks {
		for _, ref := range refs {
			out[event] = append(out[event], ref.Command)
		}
	}
	return out
}
