package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type autopilotTestEntry struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	TimeoutMs  int    `json:"timeoutMs"`
	Matcher    string `json:"matcher"`
	Enabled    *bool  `json:"enabled"`
	FailClosed bool   `json:"failClosed"`
}

func decodeAutopilotSettings(t *testing.T, path string) (map[string]json.RawMessage, map[string][]autopilotTestEntry) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, data)
	}
	hooks := map[string][]autopilotTestEntry{}
	if raw, ok := top["hooks"]; ok {
		var events map[string]json.RawMessage
		if err := json.Unmarshal(raw, &events); err != nil {
			t.Fatalf("hooks block: %v\n%s", err, raw)
		}
		for event, list := range events {
			var entries []autopilotTestEntry
			if json.Unmarshal(list, &entries) == nil {
				hooks[event] = entries
			}
		}
	}
	return top, hooks
}

func installAutopilotForTest(t *testing.T, path string) {
	t.Helper()
	if err := installAutopilotSettings(path, "/opt/beacon/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installAutopilotSettings: %v", err)
	}
}

// The shape Autopilot reads: an event maps to a flat list of {id, command, timeoutMs}, not Claude
// Code's matcher groups. A Claude-shaped group has no top-level `command`, so Autopilot's loader
// drops it as malformed and the install would collect nothing while reporting success.
func TestAutopilotInstallWritesAutopilotsFlatShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	installAutopilotForTest(t, path)

	_, hooks := decodeAutopilotSettings(t, path)
	want := map[string]struct {
		id, subcommand string
		timeout        int
	}{
		"SessionStart":      {"beacon-session-start", "session-start", 0},
		"UserPromptSubmit":  {"beacon-prompt", "prompt-submit", 30000},
		"PreToolUse":        {"beacon-pre-tool", "pre-tool", 10000},
		"PostToolUse":       {"beacon-post-tool", "post-tool", 10000},
		"PermissionRequest": {"beacon-permission", "permission-request", 10000},
		"PreCompact":        {"beacon-pre-compact", "pre-compact", 0},
		"Stop":              {"beacon-stop", "stop", 45000},
		"SessionEnd":        {"beacon-session-end", "session-end", 0},
	}
	if len(hooks) != len(want) {
		t.Fatalf("installed events = %v, want exactly %d", hooks, len(want))
	}
	for event, w := range want {
		entries := hooks[event]
		if len(entries) != 1 {
			t.Fatalf("%s: got %d entries, want 1", event, len(entries))
		}
		entry := entries[0]
		if entry.ID != w.id {
			t.Errorf("%s id = %q, want %q", event, entry.ID, w.id)
		}
		wantCommand := "'/opt/beacon/beacon-hooks' --platform autopilot --log '/tmp/runtime.jsonl' --config '/tmp/config.json' " + w.subcommand
		if entry.Command != wantCommand {
			t.Errorf("%s command = %q, want %q", event, entry.Command, wantCommand)
		}
		if entry.TimeoutMs != w.timeout {
			t.Errorf("%s timeoutMs = %d, want %d", event, entry.TimeoutMs, w.timeout)
		}
		if entry.Matcher != "" {
			t.Errorf("%s matcher = %q, want none so every tool matches", event, entry.Matcher)
		}
		if entry.FailClosed {
			t.Errorf("%s is failClosed; a telemetry hook must never block a call when it fails", event)
		}
	}
}

// Autopilot fires PostToolUse for failures too, and PostToolUseFailure beside it. Binding post-tool
// to both would record every failure twice.
func TestAutopilotInstallDoesNotBindPostToolUseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	installAutopilotForTest(t, path)
	_, hooks := decodeAutopilotSettings(t, path)
	if entries, ok := hooks["PostToolUseFailure"]; ok {
		t.Fatalf("PostToolUseFailure bound: %#v", entries)
	}
}

func TestAutopilotInstallPreservesUserSettingsAndHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "theme": "dark",
  "hooks": {
    "PreToolUse": [
      { "id": "guard-env", "matcher": "write|edit", "command": "./guard.sh", "failClosed": true, "description": "no .env" }
    ],
    "Stop": [ { "command": "printf '\\a'", "enabled": false } ],
    "SomeFutureEvent": { "not": "an array" }
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	installAutopilotForTest(t, path)

	top, hooks := decodeAutopilotSettings(t, path)
	if string(top["theme"]) != `"dark"` {
		t.Fatalf("theme = %s, want the user's value kept", top["theme"])
	}
	pre := hooks["PreToolUse"]
	if len(pre) != 2 || pre[0].ID != "guard-env" || pre[0].Matcher != "write|edit" || !pre[0].FailClosed || pre[1].ID != "beacon-pre-tool" {
		t.Fatalf("PreToolUse = %#v, want the user's guard first, unchanged, then Beacon's", pre)
	}
	stop := hooks["Stop"]
	if len(stop) != 2 || stop[0].Enabled == nil || *stop[0].Enabled || stop[1].ID != "beacon-stop" {
		t.Fatalf("Stop = %#v, want the user's disabled bell kept, then Beacon's", stop)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"description": "no .env"`) {
		t.Fatalf("a field Beacon does not model was dropped:\n%s", data)
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(top["hooks"], &events); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(strings.Fields(string(events["SomeFutureEvent"])), ""); got != `{"not":"anarray"}` {
		t.Fatalf("an event Beacon does not write was rewritten: %s", events["SomeFutureEvent"])
	}
}

func TestAutopilotReinstallIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	installAutopilotForTest(t, path)
	first, _ := os.ReadFile(path)
	installAutopilotForTest(t, path)
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("reinstall changed the file:\n%s\n---\n%s", first, second)
	}
	// A reinstall from a different binary path replaces Beacon's entries rather than adding more.
	if err := installAutopilotSettings(path, "/other/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	_, hooks := decodeAutopilotSettings(t, path)
	for event, entries := range hooks {
		if len(entries) != 1 || !strings.Contains(entries[0].Command, "/other/beacon-hooks") {
			t.Fatalf("%s = %#v, want exactly one entry from the new binary", event, entries)
		}
	}
}

// Autopilot's Beacon guide had users hand-write Claude-mode hooks under these ids. Those entries
// are the user's: Beacon recognizes its own entries by command, so it neither edits nor removes
// them, and uninstall leaves them exactly where they were.
func TestAutopilotLeavesHandWrittenClaudeModeHooksAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	handWritten := `{"hooks":{"PreToolUse":[{"id":"beacon-pre-tool","command":"beacon-hooks --platform claude pre-tool"}]}}`
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatal(err)
	}
	installAutopilotForTest(t, path)
	_, hooks := decodeAutopilotSettings(t, path)
	pre := hooks["PreToolUse"]
	if len(pre) != 2 || pre[0].Command != "beacon-hooks --platform claude pre-tool" {
		t.Fatalf("PreToolUse = %#v, want the hand-written entry kept, Beacon's after it", pre)
	}

	removed, err := removeAutopilotEndpointHooks(path)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v; want Beacon's entries removed", removed, err)
	}
	_, hooks = decodeAutopilotSettings(t, path)
	if len(hooks) != 1 || len(hooks["PreToolUse"]) != 1 || hooks["PreToolUse"][0].Command != "beacon-hooks --platform claude pre-tool" {
		t.Fatalf("after uninstall hooks = %#v, want only the hand-written entry", hooks)
	}
	if isAutopilotInstalledAt(path) {
		t.Fatal("isAutopilotInstalledAt = true after uninstall; a hand-written Claude-mode hook is not Beacon's install")
	}
}

func TestAutopilotUninstallRemovesOnlyBeaconAndEmptyBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installAutopilotForTest(t, path)
	if !isAutopilotInstalledAt(path) {
		t.Fatal("not installed after install")
	}
	removed, err := removeAutopilotEndpointHooks(path)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	top, _ := decodeAutopilotSettings(t, path)
	if _, ok := top["hooks"]; ok {
		t.Fatalf("an emptied hooks block was left behind: %v", top)
	}
	if string(top["model"]) != `"x"` {
		t.Fatalf("model = %s, want kept", top["model"])
	}
	removed, err = removeAutopilotEndpointHooks(path)
	if err != nil || removed {
		t.Fatalf("second remove = %v, %v; want nothing to remove", removed, err)
	}
	if removed, err := removeAutopilotEndpointHooks(filepath.Join(t.TempDir(), "missing.json")); err != nil || removed {
		t.Fatalf("remove on a missing file = %v, %v", removed, err)
	}
}

// The shapes Autopilot's own writer refuses. Autopilot ignores such a file when loading hooks, so
// writing Beacon's entries into a replacement would discard what the user had.
func TestAutopilotInstallRefusesMalformedSettings(t *testing.T) {
	for name, content := range map[string]string{
		"invalid JSON":        `{"hooks": `,
		"not an object":       `[1, 2]`,
		"hooks not an object": `{"hooks": []}`,
		"event not an array":  `{"hooks": {"PreToolUse": {"command": "x"}}}`,
		"string event":        `{"hooks": {"Stop": "nope"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := installAutopilotSettings(path, "/b", "/l", "/c"); err == nil {
				t.Fatal("install succeeded; want a refusal")
			}
			after, _ := os.ReadFile(path)
			if string(after) != content {
				t.Fatalf("a refused install modified the file: %s", after)
			}
		})
	}
}

func TestAutopilotSettingsPathFollowsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := AutopilotSettingsPath(LevelUser)
	if err != nil || got != filepath.Join(home, ".config", "autopilot", "settings.json") {
		t.Fatalf("AutopilotSettingsPath = %q, %v; want ~/.config/autopilot/settings.json", got, err)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err = AutopilotSettingsPath(LevelUser)
	if err != nil || got != filepath.Join(xdg, "autopilot", "settings.json") {
		t.Fatalf("AutopilotSettingsPath = %q, %v; want $XDG_CONFIG_HOME/autopilot/settings.json", got, err)
	}
	wd, _ := os.Getwd()
	got, err = AutopilotSettingsPath(LevelProject)
	if err != nil || got != filepath.Join(wd, ".autopilot", "settings.json") {
		t.Fatalf("project path = %q, %v", got, err)
	}
}
