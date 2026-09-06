package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// museInstallDir sets up a temp Muse config directory and returns the managed hooks path Beacon
// would write into it. XDG_CONFIG_HOME is cleared as well as HOME because museConfigDir prefers it,
// and a developer with it set would otherwise have these tests resolve outside the temp directory.
func museInstallDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", "muse")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create Muse config dir: %v", err)
	}
	return filepath.Join(dir, museManagedHookFileName)
}

func readMuseHooksFile(t *testing.T, path string) museHooksFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Muse hooks: %v", err)
	}
	var hooks museHooksFile
	if err := json.Unmarshal(data, &hooks); err != nil {
		t.Fatalf("decode Muse hooks: %v", err)
	}
	return hooks
}

// Every non-LLM event Muse emits gets a hook, and each carries the endpoint settings as flags. The
// event names are the wire contract: a typo in one of them is not an error, it is that event
// silently never firing.
func TestInstallMuseHooksBindsEveryNonLLMEvent(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}

	hooks := readMuseHooksFile(t, path)
	if hooks.Beacon != museManagedHookMarker {
		t.Fatalf("managed marker = %q, want %q", hooks.Beacon, museManagedHookMarker)
	}
	wantCommands := map[string]string{
		"SessionStart":      "session-start",
		"UserPromptSubmit":  "prompt-submit",
		"PreToolUse":        "pre-tool",
		"PermissionRequest": "permission-request",
		"PostToolUse":       "post-tool",
		"PreCompact":        "pre-compact",
		"PostCompact":       "post-compact",
		"SubagentStart":     "subagent-start",
		"SubagentStop":      "subagent-stop",
		"Stop":              "stop",
	}
	if len(hooks.Hooks) != len(wantCommands) {
		t.Fatalf("bound %d events, want %d: %v", len(hooks.Hooks), len(wantCommands), hooks.Hooks)
	}
	for eventName, subcommand := range wantCommands {
		groups := hooks.Hooks[eventName]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hook shape = %#v, want one command hook", eventName, groups)
		}
		command := groups[0].Hooks[0].Command
		for _, want := range []string{
			"--platform muse",
			"--log '/tmp/runtime.jsonl'",
			"--config '/tmp/config.json'",
			" " + subcommand,
		} {
			if !strings.Contains(command, want) {
				t.Fatalf("%s command missing %q:\n%s", eventName, want, command)
			}
		}
		if groups[0].Hooks[0].Type != "command" {
			t.Fatalf("%s handler type = %q, want command", eventName, groups[0].Hooks[0].Type)
		}
	}
}

// The two model-call events are excluded on purpose. Both carry the full messages array and the
// tool schemas -- the whole conversation on every model call -- and PostLLMCall is expected to fire
// per response chunk. Subscribing to either would quietly turn a telemetry install into a full
// transcript capture, so the exclusion is pinned rather than left to a reader of the map.
func TestInstallMuseHooksDoesNotSubscribeToModelCallEvents(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	hooks := readMuseHooksFile(t, path)
	for _, event := range []string{"PreLLMCall", "PostLLMCall"} {
		if _, ok := hooks.Hooks[event]; ok {
			t.Fatalf("%s is bound; it carries the entire conversation on every model call", event)
		}
	}
}

// Muse reads `timeout` as seconds. The same field is milliseconds on Qwen and the two settings
// files look alike, so this is the assertion that keeps a copy-paste from another installer from
// turning every Muse timeout into a few milliseconds -- which kills each hook mid-write while the
// install still reports success.
func TestMuseTimeoutsAreSecondsNotMilliseconds(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	hooks := readMuseHooksFile(t, path)
	for event, want := range map[string]int{
		"UserPromptSubmit":  musePromptSubmitTimeoutSeconds,
		"PreToolUse":        museToolTimeoutSeconds,
		"PermissionRequest": museToolTimeoutSeconds,
		"PostToolUse":       museToolTimeoutSeconds,
		"Stop":              museStopTimeoutSeconds,
	} {
		got := hooks.Hooks[event][0].Hooks[0].Timeout
		if got != want {
			t.Fatalf("%s timeout = %d, want %d seconds", event, got, want)
		}
		if got > 300 {
			t.Fatalf("%s timeout = %d; that is a milliseconds value in a seconds field", event, got)
		}
	}
}

// Muse deserializes matcher groups and handler objects strictly and rejects a file with an
// unrecognized key in either -- silently, skipping that whole event. This asserts on the emitted
// JSON rather than on the Go types, because the types are only a way of producing this shape and it
// is the shape Muse actually reads.
func TestInstallMuseHooksEmitsOnlyAcceptedKeys(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Muse hooks: %v", err)
	}
	var raw struct {
		Hooks map[string][]map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode Muse hooks: %v", err)
	}
	acceptedGroupKeys := map[string]bool{"matcher": true, "hooks": true}
	acceptedHandlerKeys := map[string]bool{
		"type": true, "command": true, "commandWindows": true,
		"timeout": true, "statusMessage": true, "silent": true, "async": true,
	}
	for event, groups := range raw.Hooks {
		for _, group := range groups {
			for key := range group {
				if !acceptedGroupKeys[key] {
					t.Fatalf("%s group carries key %q; Muse skips the whole event", event, key)
				}
			}
			var handlers []map[string]json.RawMessage
			if err := json.Unmarshal(group["hooks"], &handlers); err != nil {
				t.Fatalf("%s handlers: %v", event, err)
			}
			for _, handler := range handlers {
				for key := range handler {
					if !acceptedHandlerKeys[key] {
						t.Fatalf("%s handler carries key %q; Muse skips the whole event", event, key)
					}
				}
			}
		}
	}
}

// The nesting is the part that fails silently when it is wrong: handlers listed directly under an
// event name, or event names hoisted out of the `hooks` wrapper, produce a file Muse ignores
// entirely with no warning, no exit code and no log line.
func TestInstallMuseHooksKeepsTheMatcherGroupNesting(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Muse hooks: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode Muse hooks: %v", err)
	}
	if _, ok := raw["hooks"]; !ok {
		t.Fatal("no `hooks` wrapper; Muse ignores the file in silence")
	}
	for _, event := range []string{"PreToolUse", "Stop"} {
		if _, hoisted := raw[event]; hoisted {
			t.Fatalf("%s is hoisted to the top level; Muse ignores the file in silence", event)
		}
	}
	var groups []museHookGroup
	if err := json.Unmarshal(mustNestedHooks(t, data)["PreToolUse"], &groups); err != nil {
		t.Fatalf("PreToolUse is not a list of matcher groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Matcher != museAllEventsMatcher || len(groups[0].Hooks) != 1 {
		t.Fatalf("PreToolUse group = %#v, want one matcher group holding one handler", groups)
	}
}

func mustNestedHooks(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var raw struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode Muse hooks: %v", err)
	}
	return raw.Hooks
}

// Writing the hooks file is only half an install: Muse reads it because settings.json names it.
// A hooks file with nothing pointing at it is a file Muse never opens.
func TestInstallMuseHooksPointsSettingsAtTheManagedFile(t *testing.T) {
	path := museInstallDir(t)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	settings := readMuseSettingsForTest(t, museSettingsPathForHooks(path))
	if got := museManagedHooksPathValue(settings); got != path {
		t.Fatalf("%s = %q, want %q", museManagedHooksKey, got, path)
	}
	if !isMuseInstalledAt(path) {
		t.Fatal("isMuseInstalledAt = false after a complete install")
	}
}

// writeMuseSettings seeds a settings.json from a value map.
//
// Marshalled rather than concatenated into a JSON string literal, because these tests seed the key
// with a filesystem path and on Windows that path is `C:\Users\...` -- where `\U` is an invalid
// JSON string escape. Building the file by hand made the settings unreadable on Windows only, so
// every assertion downstream failed for a reason that had nothing to do with what it was testing.
func writeMuseSettings(t *testing.T, path string, values map[string]interface{}) {
	t.Helper()
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal Muse settings: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
}

func readMuseSettingsForTest(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	settings, err := readMuseSettings(path)
	if err != nil {
		t.Fatalf("read Muse settings: %v", err)
	}
	return settings
}

// Beacon is a guest in settings.json and edits one key of it. Anything else in the file -- including
// settings Beacon has never heard of -- survives the round trip.
func TestInstallMuseHooksPreservesOtherSettings(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	original := `{"telemetry":{"destination":"legacy"},"model":"muse-spark-1.3","unknown_future_key":[1,2,3]}`
	if err := os.WriteFile(settingsPath, []byte(original), 0600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}

	settings := readMuseSettingsForTest(t, settingsPath)
	for key, want := range map[string]string{
		"telemetry":          `{"destination":"legacy"}`,
		"model":              `"muse-spark-1.3"`,
		"unknown_future_key": `[1,2,3]`,
	} {
		got, ok := settings[key]
		if !ok {
			t.Fatalf("%q was dropped from settings.json", key)
		}
		if strings.ReplaceAll(strings.ReplaceAll(string(got), " ", ""), "\n", "") != want {
			t.Fatalf("%q = %s, want %s", key, got, want)
		}
	}
}

// Muse reads exactly one managed hooks file, so claiming the key when somebody else already holds
// it would disable their registration -- and disable it the way Muse does everything, without a
// word. Refusing is the only answer that cannot silently take someone's hooks away.
func TestInstallMuseHooksRefusesToStealAnExistingRegistration(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	theirs := filepath.Join(filepath.Dir(path), "their-hooks.json")
	writeMuseSettings(t, settingsPath, map[string]interface{}{museManagedHooksKey: theirs})

	err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("installMuseHooks succeeded over somebody else's managed hooks registration")
	}
	if !strings.Contains(err.Error(), theirs) {
		t.Fatalf("error does not name the existing registration: %v", err)
	}
	// Nothing is written when the check fails, so a refused install leaves no half-installed file
	// behind for a later status to find.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a refused install left a hooks file behind: %v", statErr)
	}
	settings := readMuseSettingsForTest(t, settingsPath)
	if got := museManagedHooksPathValue(settings); got != theirs {
		t.Fatalf("%s = %q, want the existing registration %q untouched", museManagedHooksKey, got, theirs)
	}
}

// Re-running an install is not somebody else's registration. The key already names Beacon's file,
// so a repair or an upgrade proceeds.
func TestInstallMuseHooksIsIdempotent(t *testing.T) {
	path := museInstallDir(t)
	for i := range 2 {
		if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatalf("installMuseHooks run %d returned error: %v", i+1, err)
		}
	}
	if !isMuseInstalledAt(path) {
		t.Fatal("isMuseInstalledAt = false after two installs")
	}
	settings := readMuseSettingsForTest(t, museSettingsPathForHooks(path))
	if got := museManagedHooksPathValue(settings); got != path {
		t.Fatalf("%s = %q, want %q", museManagedHooksKey, got, path)
	}
}

// An uninstall removes what it added and nothing more.
func TestRemoveMuseHooksClearsBothHalves(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	if err := os.WriteFile(settingsPath, []byte(`{"model":"muse-spark-1.3"}`), 0600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}

	changed, err := removeMuseHooks(path)
	if err != nil {
		t.Fatalf("removeMuseHooks returned error: %v", err)
	}
	if !changed {
		t.Fatal("removeMuseHooks reported no change after an install")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("managed hooks file survived uninstall: %v", err)
	}
	settings := readMuseSettingsForTest(t, settingsPath)
	if _, ok := settings[museManagedHooksKey]; ok {
		t.Fatal("uninstall left a managed_hooks_path pointing at a deleted file")
	}
	if _, ok := settings["model"]; !ok {
		t.Fatal("uninstall dropped an unrelated setting")
	}
	if isMuseInstalledAt(path) {
		t.Fatal("isMuseInstalledAt = true after uninstall")
	}
}

// A hooks file Beacon did not write is not Beacon's to delete, whatever it is called.
func TestRemoveMuseHooksLeavesAForeignFileAlone(t *testing.T) {
	path := museInstallDir(t)
	foreign := `{"schema_version":1,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
	if err := os.WriteFile(path, []byte(foreign), 0644); err != nil {
		t.Fatalf("write foreign hooks: %v", err)
	}

	changed, err := removeMuseHooks(path)
	if err != nil {
		t.Fatalf("removeMuseHooks returned error: %v", err)
	}
	if changed {
		t.Fatal("removeMuseHooks deleted a file Beacon did not write")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("foreign hooks file was removed: %v", err)
	}
	if string(data) != foreign {
		t.Fatalf("foreign hooks file was rewritten:\n%s", data)
	}
}

// If something else claimed the key after Beacon's install, that is now a live registration
// belonging to somebody -- so the uninstall removes Beacon's own file and leaves the key alone.
// Clearing it would break their hooks on the way out.
func TestRemoveMuseHooksDoesNotClearSomebodyElsesRegistration(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error: %v", err)
	}
	theirs := filepath.Join(filepath.Dir(path), "their-hooks.json")
	writeMuseSettings(t, settingsPath, map[string]interface{}{museManagedHooksKey: theirs})

	if _, err := removeMuseHooks(path); err != nil {
		t.Fatalf("removeMuseHooks returned error: %v", err)
	}
	settings := readMuseSettingsForTest(t, settingsPath)
	if got := museManagedHooksPathValue(settings); got != theirs {
		t.Fatalf("%s = %q, want the newer registration %q left in place", museManagedHooksKey, got, theirs)
	}
}

// Either half alone is a broken install that would otherwise report as working: a hooks file with
// no settings key is never read, and a settings key with no file behind it registers nothing. Muse
// gives no feedback in either case.
func TestIsMuseInstalledRequiresBothHalves(t *testing.T) {
	t.Run("hooks file without the settings key", func(t *testing.T) {
		path := museInstallDir(t)
		if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatalf("installMuseHooks returned error: %v", err)
		}
		if err := os.WriteFile(museSettingsPathForHooks(path), []byte(`{}`), 0600); err != nil {
			t.Fatalf("clear settings: %v", err)
		}
		if isMuseInstalledAt(path) {
			t.Fatal("isMuseInstalledAt = true for a hooks file nothing points at")
		}
	})

	t.Run("settings key without the hooks file", func(t *testing.T) {
		path := museInstallDir(t)
		if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
			t.Fatalf("installMuseHooks returned error: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove hooks file: %v", err)
		}
		if isMuseInstalledAt(path) {
			t.Fatal("isMuseInstalledAt = true for a registration with nothing behind it")
		}
	})
}

// XDG_CONFIG_HOME wins, matching how Muse itself resolves the directory. A user who sets it and
// gets an install under ~/.config/muse has a hooks file Muse never looks at.
func TestMuseConfigDirPrefersXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir, err := museConfigDir(LevelUser)
	if err != nil {
		t.Fatalf("museConfigDir returned error: %v", err)
	}
	if want := filepath.Join(xdg, "muse"); dir != want {
		t.Fatalf("museConfigDir = %q, want %q", dir, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	dir, err = museConfigDir(LevelUser)
	if err != nil {
		t.Fatalf("museConfigDir returned error: %v", err)
	}
	if want := filepath.Join(home, ".config", "muse"); dir != want {
		t.Fatalf("museConfigDir without XDG = %q, want %q", dir, want)
	}
}

// An empty level means user scope, the same default every other runtime here uses.
func TestMuseConfigDirDefaultsToUserScope(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "")

	empty, err := museConfigDir("")
	if err != nil {
		t.Fatalf("museConfigDir(\"\") returned error: %v", err)
	}
	user, err := museConfigDir(LevelUser)
	if err != nil {
		t.Fatalf("museConfigDir(user) returned error: %v", err)
	}
	if empty != user {
		t.Fatalf("museConfigDir(\"\") = %q, want the user path %q", empty, user)
	}
}

// Project scope is refused rather than quietly given a path. Muse's project .muse/hooks.json is
// ignored by the shipping build, so a project install would create a file, report success and
// collect nothing -- and the operator would have no way to tell that from a working one.
func TestMuseConfigDirRefusesProjectScope(t *testing.T) {
	_, err := museConfigDir(LevelProject)
	if err == nil {
		t.Fatal("museConfigDir(project) succeeded; a project install collects nothing")
	}
	if !strings.Contains(err.Error(), "user scope") {
		t.Fatalf("error does not point at the scope that works: %v", err)
	}
}

// A settings.json that is not valid JSON is reported, not silently replaced. Overwriting it would
// destroy a configuration whose only problem might be a trailing comma.
func TestInstallMuseHooksReportsMalformedSettings(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	if err := os.WriteFile(settingsPath, []byte(`{"model": "muse-spark-1.3",}`), 0600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("installMuseHooks succeeded over a malformed settings.json")
	}
	if !strings.Contains(err.Error(), settingsPath) {
		t.Fatalf("error does not name the file that could not be read: %v", err)
	}
}

// An empty settings.json is a file somebody touched, not a broken one.
func TestInstallMuseHooksAcceptsAnEmptySettingsFile(t *testing.T) {
	path := museInstallDir(t)
	settingsPath := museSettingsPathForHooks(path)
	if err := os.WriteFile(settingsPath, nil, 0600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := installMuseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installMuseHooks returned error on an empty settings.json: %v", err)
	}
	if !isMuseInstalledAt(path) {
		t.Fatal("isMuseInstalledAt = false after installing over an empty settings.json")
	}
}

// The path comparison has to answer for paths that do not exist yet -- which is every path during
// an install and immediately after an uninstall -- so it compares cleaned strings rather than
// resolving through the filesystem.
func TestSameFilePathToleratesCosmeticDifferences(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"/config/muse/hooks.json", "/config/muse/hooks.json"},
		{"/config/muse/hooks.json", "/config/muse//hooks.json"},
		{"/config/muse/hooks.json", "/config/muse/./hooks.json"},
	} {
		if !sameFilePath(tc.a, tc.b) {
			t.Errorf("sameFilePath(%q, %q) = false, want true", tc.a, tc.b)
		}
	}
	for _, tc := range []struct{ a, b string }{
		{"", "/config/muse/hooks.json"},
		{"/config/muse/hooks.json", ""},
		{"", ""},
		{"/config/muse/hooks.json", "/config/muse/other.json"},
	} {
		if sameFilePath(tc.a, tc.b) {
			t.Errorf("sameFilePath(%q, %q) = true, want false", tc.a, tc.b)
		}
	}
}

// A Windows path survives a round trip through settings.json.
//
// This is the regression the seeding helper exists for, pinned on every platform rather than only
// where it broke. `C:\Users\...` is a perfectly ordinary value for this key, and it is also a string
// no JSON literal can carry verbatim -- `\U` is not a valid escape. Building the file by hand made
// settings.json unparseable on Windows alone, so the guard tests downstream failed for a reason that
// had nothing to do with the guard.
func TestMuseSettingsRoundTripAWindowsPath(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	windowsPath := `C:\Users\runneradmin\AppData\Local\muse\beacon-endpoint-hooks.json`

	writeMuseSettings(t, settingsPath, map[string]interface{}{museManagedHooksKey: windowsPath})

	settings, err := readMuseSettings(settingsPath)
	if err != nil {
		t.Fatalf("readMuseSettings could not read a settings file holding a Windows path: %v", err)
	}
	if got := museManagedHooksPathValue(settings); got != windowsPath {
		t.Fatalf("%s = %q, want %q", museManagedHooksKey, got, windowsPath)
	}
}
