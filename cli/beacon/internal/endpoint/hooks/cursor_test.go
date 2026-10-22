package hooks

import (
	"encoding/json"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallCursorHooksJSONPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "echo keep") {
		t.Fatalf("non-Beacon hook was not preserved: %s", text)
	}
	// The endpoint marker used to be an env prefix. It is now the settings flags themselves, which
	// the two checks below assert, plus the platform this hook was installed for.
	if !strings.Contains(text, "--platform cursor") {
		t.Fatalf("endpoint hook was not installed for cursor: %s", text)
	}
	if !strings.Contains(text, "--log '/tmp/runtime.jsonl'") {
		t.Fatalf("endpoint log env was not added: %s", text)
	}
	if !strings.Contains(text, "--config '/tmp/config.json'") {
		t.Fatalf("endpoint config env was not added: %s", text)
	}
	if !strings.Contains(text, `"afterAgentThought"`) || !strings.Contains(text, "agent-thought") {
		t.Fatalf("afterAgentThought reasoning hook was not installed: %s", text)
	}
	var parsed struct {
		Hooks map[string][]HookRef `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	responses := parsed.Hooks["afterAgentResponse"]
	if len(responses) != 1 || !strings.HasSuffix(responses[0].Command, " agent-response") || !strings.Contains(responses[0].Command, "--platform cursor") {
		t.Fatalf("afterAgentResponse hooks = %#v, want one Beacon agent-response command", responses)
	}
}

func TestInstallCursorHooksJSONAgentResponseIsIdempotentAndRemovable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"afterAgentResponse":[{"command":"echo mine"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), " agent-response"); n != 1 {
		t.Fatalf("agent-response installed %d times after two installs, want 1: %s", n, data)
	}
	if _, err := removeEndpointHooks(path); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "agent-response") || !strings.Contains(string(data), "echo mine") {
		t.Fatalf("uninstall should remove only Beacon's afterAgentResponse hook: %s", data)
	}
}

func TestCursorTargetDirProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := cursorTargetDir(LevelProject)
	if err != nil {
		t.Fatalf("cursorTargetDir returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".cursor"); got != want {
		t.Fatalf("project target dir = %q, want %q", got, want)
	}
}

func TestReadCursorHooksFileReturnsCorruptJSONError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt hooks.json: %v", err)
	}

	if _, err := readCursorHooksFile(path); err == nil {
		t.Fatal("expected corrupt hooks.json error")
	}
}

func TestRemoveEndpointHooksDeletesFileWhenOnlyBeaconHooksRemain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks session-start"}],"stop":[{"command":"beacon-hooks stop"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected endpoint hook removal")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hooks.json still exists or unexpected error: %v", err)
	}
}

func TestInstallCursorHooksJSONReplacesExistingBeaconHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"preToolUse":[{"command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks pre-tool"},{"command":"echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installCursorHooksJSON(path, "/tmp/new beacon-hooks", "/tmp/runtime's.jsonl", "/tmp/config path.json"); err != nil {
		t.Fatalf("installCursorHooksJSON returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if strings.Contains(string(data), "old-beacon-hooks") {
		t.Fatalf("old endpoint hook was not replaced: %s", string(data))
	}
	if !strings.Contains(string(data), "echo keep") {
		t.Fatalf("non-endpoint hook was not preserved: %s", string(data))
	}
	if !strings.Contains(string(data), "'/tmp/new beacon-hooks'") || !strings.Contains(string(data), "--log ") || !strings.Contains(string(data), "runtime") {
		t.Fatalf("command values were not shell-quoted: %s", string(data))
	}
	if !strings.Contains(string(data), "--config '/tmp/config path.json'") {
		t.Fatalf("endpoint config path was not shell-quoted: %s", string(data))
	}
}

func TestInstallCursorHooksJSONDoesNotReplaceUserCommandWithBeaconEnvOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"version":1,"hooks":{"sessionStart":[{"command":"BEACON_ENDPOINT_MODE=1 echo keep"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installCursorHooksJSON returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if !strings.Contains(string(data), "BEACON_ENDPOINT_MODE=1 echo keep") {
		t.Fatalf("user hook with Beacon-like env token was removed: %s", string(data))
	}
}

func TestInstallCursorWithUnknownLevelFailsBeforeWritingTarget(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	status, err := InstallCursor(CursorOptions{Level: Level("workspace"), UserMode: true})
	if err == nil {
		t.Fatal("expected unknown hook level error")
	}
	if status.Installed {
		t.Fatalf("status should not be installed on error: %#v", status)
	}
}

// TestCursorHooksJSONKeepsFieldsBeaconDoesNotModel installs and then removes Beacon's hooks over a
// hooks.json that uses Cursor options Beacon has no fields for. Everything that is not Beacon's must
// come back with the same value: dropping `failClosed` would make a user's blocking hook fail open,
// and a prompt-type hook decoded into a command struct would be written back as an empty command.
func TestCursorHooksJSONKeepsFieldsBeaconDoesNotModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{
  "version": 1,
  "$schema": "https://example.invalid/cursor-hooks.schema.json",
  "x-team": {"owner": "secops", "rev": [1, 2]},
  "hooks": {
    "preToolUse": [
      {"command": "./guard.sh", "matcher": "Shell", "failClosed": true, "timeout": 5, "x-note": "keep"},
      {"type": "prompt", "prompt": "Block edits outside src/", "model": "fast", "failClosed": true}
    ],
    "stop": [
      {"command": "./stop.sh", "loop_limit": null},
      {"command": "BEACON_ENDPOINT_MODE=1 beacon-hooks stop"}
    ],
    "beforeReadFile": [
      {"type": "prompt", "prompt": "Never read .env files"},
      {"command": "./audit.sh", "loop_limit": 3, "failClosed": false}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{}
	if err := json.Unmarshal([]byte(existing), &want); err != nil {
		t.Fatal(err)
	}
	// The legacy Beacon stop hook is Beacon's own and is expected to go.
	wantHooks := want["hooks"].(map[string]any)
	wantHooks["stop"] = wantHooks["stop"].([]any)[:1]

	if err := installCursorHooksJSON(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := readJSONMap(t, path)
	for key, value := range want {
		if key == "hooks" {
			continue
		}
		if !reflect.DeepEqual(installed[key], value) {
			t.Fatalf("install changed top-level %q: got %#v want %#v", key, installed[key], value)
		}
	}
	installedHooks := installed["hooks"].(map[string]any)
	for event, entries := range wantHooks {
		var userEntries []any
		for _, entry := range installedHooks[event].([]any) {
			command, _ := entry.(map[string]any)["command"].(string)
			if !isEndpointHook(command) {
				userEntries = append(userEntries, entry)
			}
		}
		if !reflect.DeepEqual(userEntries, entries) {
			t.Fatalf("install changed user hooks for %q:\n got %#v\nwant %#v", event, userEntries, entries)
		}
	}
	if !isCursorInstalledAt(path) {
		t.Fatal("Beacon hooks were not detected after install")
	}

	changed, err := removeEndpointHooks(path)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !changed {
		t.Fatal("expected uninstall to remove Beacon hooks")
	}
	if got := readJSONMap(t, path); !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("install then uninstall did not restore the user's hooks.json:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return out
}
