package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// settingsHooksRuntimes are the runtimes whose hook config goes through the shared settings.json
// rewriter. Each of them shares the file with hooks the user wrote, so each must leave those alone.
var settingsHooksRuntimes = []struct {
	platform  string
	install   func(path, binaryPath, logPath, configPath string) error
	uninstall func(path string) (bool, error)
}{
	{"claude", installClaudeSettings, removeClaudeEndpointHooks},
	{"codex", installCodexHooks, removeCodexEndpointHooks},
	{"factory", installFactorySettings, removeFactoryEndpointHooks},
	{"qwen", installQwenSettings, removeQwenEndpointHooks},
}

// foreignSettingsHooksFixture is a settings file holding hook entries Beacon does not model:
// Claude Code's `http`, `prompt` and `agent` handler types, command hooks with fields Beacon has no
// struct field for, and group- and event-level shapes Beacon never writes itself. The PostToolUse
// group also holds a stale Beacon hook for the platform under test, so the rewriter has to edit
// that group rather than pass it through untouched.
func foreignSettingsHooksFixture(platform string) string {
	return `{
  "env": {"EXISTING": "1"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Skill", "hooks": [
        {"type": "http", "url": "http://localhost:31337/hooks/skill-guard"}
      ]}
    ],
    "PostToolUse": [
      {"matcher": "Bash", "x-owner": "guard", "hooks": [
        {"type": "command", "command": "BEACON_ENDPOINT_MODE=1 old-beacon-hooks --platform ` + platform + ` post-tool"},
        {"type": "http", "url": "http://localhost:31337/hooks/post", "timeout": 30, "headers": {"Authorization": "Bearer $GUARD_TOKEN"}, "allowedEnvVars": ["GUARD_TOKEN"]}
      ]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [
        {"type": "prompt", "prompt": "Evaluate whether the task is complete: $ARGUMENTS", "model": "claude-haiku-4-5", "timeout": 20},
        {"type": "agent", "prompt": "Verify the tests pass.", "statusMessage": "Verifying"}
      ]}
    ],
    "SessionStart": [
      {"hooks": [
        {"type": "command", "command": "echo keep", "async": true, "statusMessage": "Warming up", "once": true}
      ]}
    ],
    "Notification": [
      {"matcher": "idle_prompt", "hooks": []}
    ]
  }
}`
}

// foreignSettingsHookEntries are the entries from foreignSettingsHooksFixture that must come out of
// every rewrite exactly as they went in, keyed by the event they belong to.
var foreignSettingsHookEntries = map[string][]string{
	"PreToolUse":   {`{"type": "http", "url": "http://localhost:31337/hooks/skill-guard"}`},
	"PostToolUse":  {`{"type": "http", "url": "http://localhost:31337/hooks/post", "timeout": 30, "headers": {"Authorization": "Bearer $GUARD_TOKEN"}, "allowedEnvVars": ["GUARD_TOKEN"]}`},
	"Stop":         {`{"type": "prompt", "prompt": "Evaluate whether the task is complete: $ARGUMENTS", "model": "claude-haiku-4-5", "timeout": 20}`, `{"type": "agent", "prompt": "Verify the tests pass.", "statusMessage": "Verifying"}`},
	"SessionStart": {`{"type": "command", "command": "echo keep", "async": true, "statusMessage": "Warming up", "once": true}`},
}

func decodeSettingsForTest(t *testing.T, data []byte) map[string][]map[string]any {
	t.Helper()
	var root struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, data)
	}
	return root.Hooks
}

func hookEntriesForTest(groups []map[string]any) []map[string]any {
	var entries []map[string]any
	for _, group := range groups {
		list, _ := group["hooks"].([]any)
		for _, item := range list {
			if entry, ok := item.(map[string]any); ok {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func groupWithMatcherForTest(groups []map[string]any, matcher string) map[string]any {
	for _, group := range groups {
		if got, ok := group["matcher"].(string); ok && got == matcher {
			return group
		}
	}
	return nil
}

// assertForeignSettingsHooksPreserved checks every foreign entry and group shape from
// foreignSettingsHooksFixture survived the rewrite field for field.
func assertForeignSettingsHooksPreserved(t *testing.T, platform string, data []byte) {
	t.Helper()
	hooks := decodeSettingsForTest(t, data)
	for event, wantEntries := range foreignSettingsHookEntries {
		entries := hookEntriesForTest(hooks[event])
		for _, raw := range wantEntries {
			var want map[string]any
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range entries {
				if reflect.DeepEqual(entry, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: %s entry %s was not preserved verbatim; got entries %v", platform, event, raw, entries)
			}
		}
	}
	for event, groups := range hooks {
		for _, entry := range hookEntriesForTest(groups) {
			if entry["type"] == "command" {
				continue
			}
			if _, ok := entry["command"]; ok {
				t.Errorf("%s: %s %v entry gained a command field: %v", platform, event, entry["type"], entry)
			}
		}
	}

	post := groupWithMatcherForTest(hooks["PostToolUse"], "Bash")
	if post == nil {
		t.Fatalf("%s: PostToolUse group with matcher Bash was dropped:\n%s", platform, data)
	}
	if post["x-owner"] != "guard" {
		t.Errorf("%s: PostToolUse group lost its x-owner field: %v", platform, post)
	}
	if got := len(hookEntriesForTest([]map[string]any{post})); got != 1 {
		t.Errorf("%s: PostToolUse Bash group has %d entries, want only the http hook: %v", platform, got, post)
	}
	if strings.Contains(string(data), "old-beacon-hooks") {
		t.Errorf("%s: stale Beacon hook was not removed:\n%s", platform, data)
	}
	if groupWithMatcherForTest(hooks["Stop"], "") == nil {
		t.Errorf("%s: Stop group lost its empty matcher:\n%s", platform, data)
	}
	notification := groupWithMatcherForTest(hooks["Notification"], "idle_prompt")
	if notification == nil {
		t.Errorf("%s: Notification group with an empty hooks list was dropped:\n%s", platform, data)
	} else if list, ok := notification["hooks"].([]any); !ok || len(list) != 0 {
		t.Errorf("%s: Notification group hooks = %#v, want an empty list", platform, notification["hooks"])
	}
}

func TestInstallSettingsHooksPreservesForeignHookEntriesVerbatim(t *testing.T) {
	for _, runtime := range settingsHooksRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(foreignSettingsHooksFixture(runtime.platform)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := runtime.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
				t.Fatalf("install returned error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertForeignSettingsHooksPreserved(t, runtime.platform, data)
			if !strings.Contains(string(data), "--platform "+runtime.platform) {
				t.Fatalf("Beacon hooks were not installed:\n%s", data)
			}
			if !strings.Contains(string(data), `"EXISTING"`) {
				t.Fatalf("non-hook settings were not preserved:\n%s", data)
			}
		})
	}
}

// A repair reruns the same install over a file that already holds Beacon's hooks; the issue saw
// that second pass strip the user's http hooks again.
func TestRepairSettingsHooksPreservesForeignHookEntriesVerbatim(t *testing.T) {
	for _, runtime := range settingsHooksRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(foreignSettingsHooksFixture(runtime.platform)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := runtime.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
				t.Fatalf("install returned error: %v", err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.install(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
				t.Fatalf("second install returned error: %v", err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertForeignSettingsHooksPreserved(t, runtime.platform, second)
			if string(first) != string(second) {
				t.Fatalf("a repeated install changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
		})
	}
}

func TestRemoveSettingsHooksPreservesForeignHookEntriesVerbatim(t *testing.T) {
	for _, runtime := range settingsHooksRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(foreignSettingsHooksFixture(runtime.platform)), 0600); err != nil {
				t.Fatal(err)
			}
			changed, err := runtime.uninstall(path)
			if err != nil {
				t.Fatalf("uninstall returned error: %v", err)
			}
			if !changed {
				t.Fatal("uninstall reported no change, want the stale Beacon hook removed")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertForeignSettingsHooksPreserved(t, runtime.platform, data)
			if strings.Contains(string(data), "--platform "+runtime.platform) {
				t.Fatalf("Beacon hook was not removed:\n%s", data)
			}
		})
	}
}

// Status and uninstall answer for their own platform only. A settings file can carry another
// runtime's Beacon hook -- a settings.json copied or imported from Claude Code brings its hooks
// along -- and claiming it would report a runtime as collecting while every event went out under
// the other platform, then have uninstall take the other runtime's telemetry with it. Driven
// through the install, status and uninstall paths the endpoint commands call, against each
// runtime's real project-scope file.
func TestSettingsRuntimesClaimOnlyTheirOwnPlatformsHooks(t *testing.T) {
	for _, tc := range []struct {
		platform, other string
		runtime         hookRuntime
	}{
		{"claude", "factory", claudeRuntime},
		{"codex", "claude", codexRuntime},
		{"factory", "claude", factoryRuntime},
		{"qwen", "claude", qwenRuntime},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			home := t.TempDir()
			testenv.SetHome(t, home)
			t.Chdir(t.TempDir())
			opts := RuntimeOptions{Level: LevelProject, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
			path, err := tc.runtime.configPath(LevelProject)
			if err != nil {
				t.Fatal(err)
			}
			theirs := `'/opt/beacon/hooks/beacon-hooks' --platform ` + tc.other + ` --log '/tmp/runtime.jsonl' pre-tool`
			seed := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"` + theirs + `"}]}]}}`
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}

			if runtimeHookStatus(tc.runtime, opts).Installed {
				t.Fatalf("status claimed a --platform %s hook as a %s install", tc.other, tc.platform)
			}
			status, err := uninstallRuntimeHooks(tc.runtime, opts)
			if err != nil {
				t.Fatalf("uninstall over another platform's hook: %v", err)
			}
			if status.Installed || !strings.Contains(readFile(t, path), theirs) {
				t.Fatalf("uninstall with nothing of %s's present reported installed=%t or removed the --platform %s hook:\n%s",
					tc.platform, status.Installed, tc.other, readFile(t, path))
			}

			if _, err := installRuntimeHooks(tc.runtime, opts); err != nil {
				t.Fatalf("install: %v", err)
			}
			if !runtimeHookStatus(tc.runtime, opts).Installed {
				t.Fatal("status does not recognize the install it just made")
			}
			if !strings.Contains(readFile(t, path), theirs) {
				t.Fatalf("install replaced the --platform %s hook:\n%s", tc.other, readFile(t, path))
			}

			if _, err := uninstallRuntimeHooks(tc.runtime, opts); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			after := readFile(t, path)
			if strings.Contains(after, "--platform "+tc.platform+" ") || runtimeHookStatus(tc.runtime, opts).Installed {
				t.Fatalf("uninstall left %s's hooks behind:\n%s", tc.platform, after)
			}
			if !strings.Contains(after, theirs) {
				t.Fatalf("uninstall removed the --platform %s hook:\n%s", tc.other, after)
			}
		})
	}
}

// Beacon's own entries are built in code, not read from disk, and must serialize exactly as they
// did before foreign entries were kept verbatim: hook commands are compared by string elsewhere.
func TestSettingsHookRefBuiltInCodeMarshalsKnownFieldsOnly(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{settingsHookRef{Type: "command", Command: "x"}, `{"type":"command","command":"x"}`},
		{settingsHookRef{Type: "command", Command: "x", Timeout: 30}, `{"type":"command","command":"x","timeout":30}`},
		{settingsHookRef{Type: "command", Command: "x", Shell: "bash"}, `{"type":"command","command":"x","shell":"bash"}`},
		{settingsHookRef{Type: "command", Command: "x", ShowOutput: new(bool)}, `{"type":"command","command":"x","show_output":false}`},
		{settingsHookGroup{Hooks: []settingsHookRef{{Type: "command", Command: "x"}}}, `{"hooks":[{"type":"command","command":"x"}]}`},
		{settingsHookGroup{Matcher: "*", Hooks: []settingsHookRef{{Type: "command", Command: "x"}}}, `{"matcher":"*","hooks":[{"type":"command","command":"x"}]}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("marshal = %s, want %s", got, tc.want)
		}
	}
}

// If code ever edits a field Beacon models on an entry it read from disk, the edit must land and
// the fields Beacon does not model must still be kept.
func TestSettingsHookRefEditedAfterReadKeepsUnmodelledFields(t *testing.T) {
	var ref settingsHookRef
	if err := json.Unmarshal([]byte(`{"type":"http","url":"http://localhost:31337/x","timeout":5}`), &ref); err != nil {
		t.Fatal(err)
	}
	ref.Timeout = 9
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "http", "url": "http://localhost:31337/x", "timeout": float64(9)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edited entry = %v, want %v", got, want)
	}

	ref.Timeout = 0
	data, err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"type": "http", "url": "http://localhost:31337/x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entry with timeout cleared = %v, want %v", got, want)
	}
}
