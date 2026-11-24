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

func TestInstallDevinProjectHooksPreservesNonBeaconHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"PreToolUse":[{"matcher":"exec","hooks":[{"type":"command","command":"echo keep"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinHooks(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"echo keep",
		"--log '/tmp/runtime.jsonl'",
		"--config '/tmp/config.json'",
		"'/tmp/beacon hooks' --platform devin",
		"PermissionRequest",
		"permission-request",
		"PreToolUse",
		"PostToolUse",
		"SessionStart",
		"SessionEnd",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Devin hooks missing %q:\n%s", want, text)
		}
	}
}

func TestInstallDevinUserConfigPreservesUnrelatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	existing := `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"theme"`) || !strings.Contains(text, "echo keep") || !strings.Contains(text, "--platform devin") {
		t.Fatalf("user config was not preserved and updated:\n%s", text)
	}
}

func TestInstallDevinUserConfigPreservesImportConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	existing := `{"theme":"dark","read_config_from":{"cursor":true,"windsurf":false,"claude":true}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinCLIHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinCLIHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal config: %v\n%s", err, data)
	}
	readConfigFrom := config["read_config_from"].(map[string]interface{})
	if readConfigFrom["claude"] != true {
		t.Fatalf("read_config_from.claude = %#v, want preserved true", readConfigFrom["claude"])
	}
	if readConfigFrom["cursor"] != true || readConfigFrom["windsurf"] != false {
		t.Fatalf("read_config_from did not preserve other values: %#v", readConfigFrom)
	}
	if config["theme"] != "dark" {
		t.Fatalf("theme = %#v, want preserved dark", config["theme"])
	}
}

func TestInstallDevinDesktopHooksUsesCascadeEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := installDevinDesktopHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinDesktopHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	for _, want := range []string{"pre_user_prompt", "post_write_code", "post_run_command", "post_mcp_tool_use", "post_read_code", "post_cascade_response", "--platform devin-desktop", "prompt-submit", "post-tool", "cascade-response"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Devin Desktop hooks missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"PreToolUse", "PostToolUse", "SessionStart", "read_config_from"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("Devin Desktop hooks should not contain %q:\n%s", notWant, text)
		}
	}
}

func TestInstallDevinDesktopCascadeResponseHookIsIdempotentAndRemovable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"post_cascade_response":[{"command":"python3 mine.py","show_output":true}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := installDevinDesktopHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", ""); err != nil {
			t.Fatal(err)
		}
	}
	var parsed struct {
		Hooks map[string][]windsurfHookRef `json:"hooks"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	var beacon []windsurfHookRef
	kept := false
	for _, ref := range parsed.Hooks["post_cascade_response"] {
		switch {
		case strings.HasSuffix(ref.Command, " cascade-response"):
			beacon = append(beacon, ref)
		case ref.Command == "python3 mine.py":
			kept = true
		}
	}
	if len(beacon) != 1 || beacon[0].ShowOutput == nil || *beacon[0].ShowOutput {
		t.Fatalf("post_cascade_response Beacon hooks = %#v, want one with show_output false", beacon)
	}
	if !kept {
		t.Fatalf("the user's own post_cascade_response hook was not kept: %s", data)
	}
	if _, err := removeDevinDesktopEndpointHooks(path); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "cascade-response") || !strings.Contains(string(data), "python3 mine.py") {
		t.Fatalf("uninstall should remove only Beacon's post_cascade_response hook: %s", data)
	}
}

func TestInstallDevinCLIHooksReplacesLegacyHooksWithExplicitPlatform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"PostToolUse":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform devin post-tool"}]},{"hooks":[{"type":"command","command":"echo keep"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinCLIHooks(path, "/tmp/new-beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinCLIHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "--platform devin post-tool") || strings.Contains(text, "old-beacon-hooks") {
		t.Fatalf("legacy devin hook was not replaced:\n%s", text)
	}
	if !strings.Contains(text, "echo keep") || !strings.Contains(text, "--platform devin-cli") {
		t.Fatalf("expected preserved hook and explicit devin-cli hook:\n%s", text)
	}
}

func TestInstallDevinDesktopHooksDoesNotRemoveCLIHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"post_write_code":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-cli post-tool"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	if err := installDevinDesktopHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installDevinDesktopHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "--platform devin-cli") || !strings.Contains(text, "--platform devin-desktop") {
		t.Fatalf("expected CLI and Desktop hooks to coexist:\n%s", text)
	}
}

func TestRemoveDevinEndpointHooksPreservesOtherHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.v1.json")
	existing := `{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]},{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin session-start"}]}]}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeDevinEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeDevinEndpointHooks returned error: %v", err)
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
		t.Fatalf("non-Beacon hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "BEACON_ENDPOINT_MODE=1") {
		t.Fatalf("endpoint hook was not removed:\n%s", text)
	}
}

func TestRemoveDevinDesktopEndpointHooksPreservesCLIHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"hooks":{"post_write_code":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-cli post-tool"},{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-desktop post-tool"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := removeDevinDesktopEndpointHooks(path)
	if err != nil {
		t.Fatalf("removeDevinDesktopEndpointHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected desktop endpoint hook removal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "--platform devin-cli") {
		t.Fatalf("Devin CLI hook was not preserved:\n%s", text)
	}
	if strings.Contains(text, "--platform devin-desktop") {
		t.Fatalf("Devin Desktop hook was not removed:\n%s", text)
	}
}

func TestReadDevinConfigReturnsCorruptJSONError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}

	if _, err := readDevinConfig(path); err == nil {
		t.Fatal("expected corrupt config error")
	}
}

func TestDevinConfigPathProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := devinConfigPath(LevelProject)
	if err != nil {
		t.Fatalf("devinConfigPath returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".devin", "hooks.v1.json"); got != want {
		t.Fatalf("project config path = %q, want %q", got, want)
	}
}

func TestDevinHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".config", "devin", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := DevinHookStatus(DevinOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("DevinHookStatus installed = false, status=%#v", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}

func TestDevinDesktopHookStatusDetectsInstalled(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".codeium", "windsurf", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"post_write_code":[{"command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform devin-desktop post-tool"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	status := DevinDesktopHookStatus(DevinDesktopOptions{Level: LevelUser, UserMode: true})
	if !status.Installed {
		t.Fatalf("DevinDesktopHookStatus installed = false, status=%#v", status)
	}
	if status.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", status.ConfigPath, path)
	}
}

func TestDevinDesktopConfigPathProjectLevel(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	target, err := devinDesktopConfigPath(LevelProject)
	if err != nil {
		t.Fatalf("devinDesktopConfigPath returned error: %v", err)
	}
	if got, want := target, filepath.Join(dir, ".windsurf", "hooks.json"); got != want {
		t.Fatalf("project config path = %q, want %q", got, want)
	}
}

// The user's own Devin hooks in the shapes Beacon has no fields for: a prompt hook, a command hook
// with a fractional timeout and a key Beacon does not model, a group with a key of its own, and a
// prompt hook sharing a group with a hook an older Beacon wrote.
const devinUserHooksFixture = `{
  "Stop": [
    {"hooks": [{"type": "prompt", "prompt": "Did the agent run the tests? Block if not.", "timeout": 10}]}
  ],
  "PreToolUse": [
    {"matcher": "exec", "description": "shell guard", "hooks": [{"type": "command", "command": "/usr/local/bin/guard", "timeout": 2.5, "owner": "platform"}]}
  ],
  "SessionStart": [
    {"hooks": [{"type": "prompt", "prompt": "Summarize the repository."}, {"type": "command", "command": "'/old/beacon-hooks' --platform devin-cli --log '/tmp/old.jsonl' session-start"}]}
  ]
}`

// #1369: install and uninstall decoded the user's entries into Beacon's own structs and wrote those
// back, so a prompt hook lost its prompt and gained an empty command, and a fractional timeout
// failed the read.
func TestDevinInstallAndUninstallKeepAUsersHooksAsWritten(t *testing.T) {
	for _, file := range []struct {
		name, content string
		standalone    bool
	}{
		{name: "config.json", content: `{"theme": "dark", "hooks": ` + devinUserHooksFixture + `}`},
		{name: "hooks.v1.json", content: devinUserHooksFixture, standalone: true},
	} {
		t.Run(file.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), file.name)
			if err := os.WriteFile(path, []byte(file.content), 0600); err != nil {
				t.Fatal(err)
			}
			for _, step := range []string{"install", "repair"} {
				if err := installDevinCLIHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
					t.Fatalf("%s failed over the user's hooks: %v", step, err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				assertDevinUserHooksKept(t, data, file.standalone)
				if !isDevinCLIInstalledAt(path) {
					t.Fatalf("after %s, status reports Beacon's hooks missing:\n%s", step, data)
				}
				if strings.Contains(string(data), "/old/beacon-hooks") {
					t.Fatalf("%s left the hook an older Beacon wrote:\n%s", step, data)
				}
			}
			changed, err := removeDevinCLIEndpointHooks(path)
			if err != nil {
				t.Fatalf("uninstall failed over the user's hooks: %v", err)
			}
			if !changed {
				t.Fatal("uninstall reported no change, want Beacon's hooks removed")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "--platform devin-cli") {
				t.Fatalf("Beacon hooks were not removed:\n%s", data)
			}
			assertDevinUserHooksKept(t, data, file.standalone)
		})
	}
}

// assertDevinUserHooksKept checks every entry and group of devinUserHooksFixture that is the
// user's, field for field, and that nothing gained an empty command.
func assertDevinUserHooksKept(t *testing.T, data []byte, standalone bool) {
	t.Helper()
	hooks := map[string][]map[string]any{}
	if standalone {
		if err := json.Unmarshal(data, &hooks); err != nil {
			t.Fatalf("hooks file is not valid JSON: %v\n%s", err, data)
		}
	} else {
		var root struct {
			Theme string                      `json:"theme"`
			Hooks map[string][]map[string]any `json:"hooks"`
		}
		if err := json.Unmarshal(data, &root); err != nil {
			t.Fatalf("config is not valid JSON: %v\n%s", err, data)
		}
		if root.Theme != "dark" {
			t.Errorf("the user's theme setting changed to %q", root.Theme)
		}
		hooks = root.Hooks
	}
	for event, want := range map[string][]map[string]any{
		"Stop":         {{"type": "prompt", "prompt": "Did the agent run the tests? Block if not.", "timeout": float64(10)}},
		"PreToolUse":   {{"type": "command", "command": "/usr/local/bin/guard", "timeout": 2.5, "owner": "platform"}},
		"SessionStart": {{"type": "prompt", "prompt": "Summarize the repository."}},
	} {
		var got []map[string]any
		for _, entry := range hookEntriesForTest(hooks[event]) {
			if command, _ := entry["command"].(string); !strings.Contains(command, "beacon-hooks") {
				got = append(got, entry)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the user's %s hooks changed: got %v, want %v\n%s", event, got, want, data)
		}
	}
	group := groupWithMatcherForTest(hooks["PreToolUse"], "exec")
	if group == nil {
		t.Fatalf("the user's PreToolUse exec group was dropped:\n%s", data)
	}
	if group["description"] != "shell guard" {
		t.Errorf("the user's group lost a key Beacon does not model: %v", group)
	}
	if !strings.Contains(string(data), `"timeout": 2.5`) {
		t.Errorf("the file no longer says \"timeout\": 2.5:\n%s", data)
	}
}
