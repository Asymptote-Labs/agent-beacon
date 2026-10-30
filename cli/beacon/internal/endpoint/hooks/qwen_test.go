package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// decodeQwenHooks reads the `hooks` block back out of a settings file as data rather than as text,
// so assertions can be about values (a timeout of 10000) instead of about substrings that would
// also match a different number containing the same digits.
func decodeQwenHooks(t *testing.T, path string) map[string][]struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Shell   string `json:"shell"`
	} `json:"hooks"`
} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
				Shell   string `json:"shell"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, data)
	}
	return settings.Hooks
}

// The divergence that makes this installer more than a copy of the Claude one.
//
// Claude Code reads `timeout` as seconds; Qwen Code reads it as milliseconds. Writing Claude's
// values into a Qwen settings file gives every tool hook ten *milliseconds* to start a process,
// read stdin, resolve a git branch and append to a JSONL file. It cannot, so each one is killed
// mid-write -- while the install reports success and the settings file looks correct. This test is
// the tripwire for that: any value small enough to be a seconds-shaped number fails it.
func TestQwenTimeoutsAreMillisecondsNotSeconds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := installQwenSettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installQwenSettings returned error: %v", err)
	}

	hooks := decodeQwenHooks(t, path)
	for event, wantMS := range map[string]int{
		"UserPromptSubmit":   qwenPromptSubmitTimeoutMS,
		"PreToolUse":         qwenToolTimeoutMS,
		"PostToolUse":        qwenToolTimeoutMS,
		"PostToolUseFailure": qwenToolTimeoutMS,
		"PermissionRequest":  qwenToolTimeoutMS,
		"Stop":               qwenStopTimeoutMS,
	} {
		groups, ok := hooks[event]
		if !ok || len(groups) == 0 || len(groups[0].Hooks) == 0 {
			t.Fatalf("%s hook was not installed: %#v", event, hooks)
		}
		got := groups[0].Hooks[0].Timeout
		if got != wantMS {
			t.Errorf("%s timeout = %d, want %d", event, got, wantMS)
		}
		// The property, independent of the exact constants: no timeout Beacon writes may be small
		// enough to be a plausible seconds value, because that is exactly what a copy of the
		// Claude installer would produce.
		if got < 1000 {
			t.Errorf("%s timeout = %d; Qwen reads this field as milliseconds, so anything under "+
				"1000 kills the hook before it can write", event, got)
		}
	}

	// Events with no explicit timeout inherit Qwen's own 60000ms default, which is ample for a
	// hook that only writes a line. Asserting the absence keeps a future edit from "helpfully"
	// adding a seconds-shaped value here.
	for _, event := range []string{"SessionStart", "SessionEnd", "SubagentStart", "SubagentStop"} {
		groups, ok := hooks[event]
		if !ok || len(groups) == 0 || len(groups[0].Hooks) == 0 {
			t.Fatalf("%s hook was not installed: %#v", event, hooks)
		}
		if got := groups[0].Hooks[0].Timeout; got != 0 {
			t.Errorf("%s timeout = %d, want it omitted so Qwen's 60000ms default applies", event, got)
		}
	}
}

// Every Qwen hook event Beacon claims to collect has to actually be registered, wired to the right
// subcommand, and carry the --platform value the adapter branches on. Tool events must carry a
// matcher Qwen recognizes as "all tools" -- `""` or `"*"`; anything else is a regex that would
// silently filter events out.
func TestInstallQwenRegistersEveryCollectedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := installQwenSettings(path, "/tmp/beacon hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installQwenSettings returned error: %v", err)
	}

	hooks := decodeQwenHooks(t, path)
	for event, wantSubcommand := range map[string]string{
		"SessionStart":       "session-start",
		"UserPromptSubmit":   "prompt-submit",
		"PreToolUse":         "pre-tool",
		"PostToolUse":        "post-tool",
		"PostToolUseFailure": "post-tool",
		"PermissionRequest":  "permission-request",
		"Stop":               "stop",
		"SubagentStart":      "subagent-start",
		"SubagentStop":       "subagent-stop",
		"SessionEnd":         "session-end",
	} {
		groups, ok := hooks[event]
		if !ok || len(groups) == 0 || len(groups[0].Hooks) == 0 {
			t.Fatalf("%s hook was not installed: %#v", event, hooks)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" {
			t.Errorf("%s hook type = %q, want command", event, hook.Type)
		}
		if !strings.Contains(hook.Command, "--platform qwen") {
			t.Errorf("%s command = %q, want --platform qwen", event, hook.Command)
		}
		if !strings.HasSuffix(hook.Command, " "+wantSubcommand) {
			t.Errorf("%s command = %q, want it to end in %q", event, hook.Command, wantSubcommand)
		}
		if !strings.Contains(hook.Command, "--log '/tmp/runtime.jsonl'") {
			t.Errorf("%s command = %q, want the endpoint log flag", event, hook.Command)
		}
		if !strings.Contains(hook.Command, "--config '/tmp/config.json'") {
			t.Errorf("%s command = %q, want the endpoint config flag", event, hook.Command)
		}
		// A path with a space in it is the ordinary case on macOS and Windows, and an unquoted one
		// would run as two arguments.
		if !strings.Contains(hook.Command, "'/tmp/beacon hooks'") {
			t.Errorf("%s command = %q, want the binary path quoted", event, hook.Command)
		}
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest"} {
		if matcher := hooks[event][0].Matcher; matcher != "*" && matcher != "" {
			t.Errorf("%s matcher = %q; Qwen treats only \"\" and \"*\" as match-all", event, matcher)
		}
	}

	// The fire-and-forget events are deliberately absent. MessageDisplay alone fires roughly every
	// 200ms for the length of every reply.
	for _, event := range []string{"MessageDisplay", "StopFailure", "PostCompact", "SessionDelete", "TodoCreated", "TodoCompleted"} {
		if _, ok := hooks[event]; ok {
			t.Errorf("%s was registered; it is deliberately not collected", event)
		}
	}
}

func TestQwenSettingsPathUserAndProjectLevels(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("QWEN_HOME", "")

	userPath, err := QwenSettingsPath(LevelUser)
	if err != nil {
		t.Fatalf("QwenSettingsPath(user) returned error: %v", err)
	}
	if want := filepath.Join(home, ".qwen", "settings.json"); userPath != want {
		t.Errorf("user settings path = %q, want %q", userPath, want)
	}
	// An empty level means the user level, which is what an unset --level flag produces.
	if defaulted, err := QwenSettingsPath(""); err != nil || defaulted != userPath {
		t.Errorf("QwenSettingsPath(\"\") = %q, %v; want the user path", defaulted, err)
	}

	project := t.TempDir()
	t.Chdir(project)
	projectPath, err := QwenSettingsPath(LevelProject)
	if err != nil {
		t.Fatalf("QwenSettingsPath(project) returned error: %v", err)
	}
	if want := filepath.Join(project, ".qwen", "settings.json"); projectPath != want {
		t.Errorf("project settings path = %q, want %q", projectPath, want)
	}

	if _, err := QwenSettingsPath(Level("nonsense")); err == nil {
		t.Error("QwenSettingsPath(nonsense) returned no error")
	}
}

// Corrupt JSON must surface as an error rather than being silently overwritten: settings.json holds
// the user's auth type and MCP servers, and clobbering it to install telemetry is not a trade
// Beacon gets to make on their behalf.
func TestQwenRefusesToOverwriteCorruptSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := readQwenSettings(path); err == nil {
		t.Fatal("readQwenSettings accepted corrupt JSON")
	}
	if err := installQwenSettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err == nil {
		t.Fatal("installQwenSettings overwrote a settings file it could not parse")
	}
	if got := readFileString(t, path); got != "{not json" {
		t.Fatalf("settings file was modified: %q", got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// The second way this installer can look correct and collect nothing.
//
// Beacon builds its hook command with hookCommandQuote, which single-quotes every path on every
// platform. That is POSIX quoting: literal in bash, and *not* valid in cmd.exe, where the quotes
// are ordinary characters and the command never resolves to an executable. Qwen picks the shell it
// runs a command hook with, and a Node-hosted runtime left to the Windows default lands on cmd.exe
// -- so without this field every hook on a Windows host fails to start while `endpoint hooks
// install` still prints success and the settings file still reads correctly.
//
// Same silent-failure shape as a seconds-valued timeout, and it gets the same tripwire. `bash` is
// the only one of Qwen's two documented values that can parse what Beacon writes; `powershell`
// would treat a quoted string in command position as an expression.
func TestQwenHooksDeclareTheShellTheirQuotingRequires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := installQwenSettings(path, `C:\Program Files\Beacon\beacon-hooks.exe`, "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installQwenSettings returned error: %v", err)
	}

	hooks := decodeQwenHooks(t, path)
	if len(hooks) == 0 {
		t.Fatal("no hooks installed")
	}
	for event, groups := range hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Shell != qwenHookShell {
					t.Errorf("%s shell = %q, want %q; Beacon's command is POSIX-quoted and will not "+
						"run under the Windows default shell", event, hook.Shell, qwenHookShell)
				}
				// The pairing this test is really about: the declared shell has to match the
				// quoting actually emitted. If hookCommandQuote ever stops single-quoting, this
				// declaration becomes a lie rather than a fix.
				if !strings.Contains(hook.Command, `'C:\Program Files\Beacon\beacon-hooks.exe'`) {
					t.Errorf("%s command = %q, want the binary single-quoted for %s", event, hook.Command, qwenHookShell)
				}
			}
		}
	}
}

// Declaring a shell must not leak into the runtimes that share this settings writer and do not
// expose the field. Claude Code has no `shell` key in its hook schema, and emitting one there
// would put an unrecognized field into a file Beacon does not own.
func TestOnlyQwenDeclaresAHookShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := installClaudeSettings(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installClaudeSettings returned error: %v", err)
	}
	if got := readFileString(t, path); strings.Contains(got, `"shell"`) {
		t.Fatalf("Claude settings carry a shell field:\n%s", got)
	}
}

// Qwen Code keeps its user directory under QWEN_HOME when that is set (getGlobalQwenDir in
// packages/core/src/config/storage.ts): a leading `~` is expanded, a relative path is made absolute
// and the directory is used as is, with no `.qwen` appended. An install that wrote ~/.qwen would
// report success while Qwen kept reading the moved directory.
func TestQwenSettingsPathHonorsQwenHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(t.TempDir(), "qwen-moved")
	for name, tc := range map[string]struct{ value, want string }{
		"absolute": {moved, filepath.Join(moved, "settings.json")},
		"tilde":    {"~/qwen-alt", filepath.Join(home, "qwen-alt", "settings.json")},
		"relative": {"qwen-rel", filepath.Join(cwd, "qwen-rel", "settings.json")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("QWEN_HOME", tc.value)
			got, err := QwenSettingsPath(LevelUser)
			if err != nil || got != tc.want {
				t.Fatalf("QwenSettingsPath(user) = %q, %v; want %q", got, err, tc.want)
			}
			if forHome, err := QwenSettingsPathForHome(LevelUser, home); err != nil || forHome != tc.want {
				t.Fatalf("QwenSettingsPathForHome(user) = %q, %v; want %q", forHome, err, tc.want)
			}
			// The project file never moves.
			projectPath, err := QwenSettingsPath(LevelProject)
			if want := filepath.Join(cwd, ".qwen", "settings.json"); err != nil || projectPath != want {
				t.Fatalf("QwenSettingsPath(project) = %q, %v; want %q", projectPath, err, want)
			}
		})
	}

	// The installer and the status check both go through the same resolver, so a hook written under
	// QWEN_HOME is the one that is reported.
	t.Setenv("QWEN_HOME", moved)
	want := filepath.Join(moved, "settings.json")
	if err := os.MkdirAll(moved, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/tmp/beacon-hooks' --platform qwen --log '/tmp/runtime.jsonl' stop"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	status := QwenHookStatus(QwenOptions{Level: LevelUser, UserMode: true})
	if !status.Installed || status.SettingsPath != want {
		t.Fatalf("QwenHookStatus = %#v, want installed at %q", status, want)
	}
	if configured, err := qwenRuntime.configPath(LevelUser); err != nil || configured != want {
		t.Fatalf("installer config path = %q, %v; want %q", configured, err, want)
	}
}
