package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// Kiro's hook loader is silent about a file it does not like, the same way OpenHands' and Muse's
// are: a hook file that fails validation is skipped and the agent goes on working with no hooks
// from it. Nothing in the runtime tells Beacon that an install stopped working, so the emitted
// shape is pinned here rather than trusted.

// kiroUserHooksPath points the user scope at a temp home and returns the path Beacon writes.
// KIRO_HOME is cleared as well as HOME because kiroHooksDir prefers it, and a developer with it
// set would otherwise have these tests resolve outside the temp directory.
func kiroUserHooksPath(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv("KIRO_HOME", "")
	path, err := kiroHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("kiroHooksPath: %v", err)
	}
	return path
}

func installKiroFixture(t *testing.T, path string) {
	t.Helper()
	if err := installKiroHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installKiroHooks returned error: %v", err)
	}
}

// readKiroFile decodes the emitted file into a loose map, so an assertion can ask about a key the
// typed struct does not have. Several of the tests below are about keys Beacon must *not* write.
func readKiroFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode hook file: %v", err)
	}
	return document
}

func kiroHookObjects(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	raw, ok := readKiroFile(t, path)["hooks"].([]interface{})
	if !ok {
		t.Fatalf("hook file has no hooks array: %#v", readKiroFile(t, path))
	}
	hooks := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		hook, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("hook entry is not an object: %#v", item)
		}
		hooks = append(hooks, hook)
	}
	return hooks
}

func kiroHookCommand(t *testing.T, hook map[string]interface{}) string {
	t.Helper()
	action, ok := hook["action"].(map[string]interface{})
	if !ok {
		t.Fatalf("hook has no action: %#v", hook)
	}
	command, _ := action["command"].(string)
	return command
}

// ---------------------------------------------------------------------------
// Fresh install
// ---------------------------------------------------------------------------

// The five triggers Beacon subscribes to, each bound to the subcommand that maps it. The trigger
// names are the wire contract: a typo in one is not an error, it is that event silently never
// firing.
func TestInstallKiroBindsEveryTrigger(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	want := map[string]string{
		"SessionStart":     "session-start",
		"UserPromptSubmit": "prompt-submit",
		"PreToolUse":       "pre-tool",
		"PostToolUse":      "post-tool",
		"Stop":             "stop",
	}
	hooks := kiroHookObjects(t, path)
	if len(hooks) != len(want) {
		t.Fatalf("hook count = %d, want exactly %d", len(hooks), len(want))
	}
	seen := map[string]bool{}
	for _, hook := range hooks {
		trigger, _ := hook["trigger"].(string)
		subcommand, ok := want[trigger]
		if !ok {
			t.Fatalf("unexpected trigger %q", trigger)
		}
		if seen[trigger] {
			t.Fatalf("trigger %q registered twice", trigger)
		}
		seen[trigger] = true

		command := kiroHookCommand(t, hook)
		if !strings.HasSuffix(command, " "+subcommand) {
			t.Errorf("trigger %q command = %q, want it to end in the %q subcommand", trigger, command, subcommand)
		}
		if !strings.Contains(command, "--platform kiro") {
			t.Errorf("trigger %q command = %q, want --platform kiro", trigger, command)
		}
		// The endpoint settings ride as flags rather than as an inline VAR=value prefix, so the
		// hook reads the right log and config wherever the runtime is running.
		for _, flag := range []string{"--log", "--config"} {
			if !strings.Contains(command, flag) {
				t.Errorf("trigger %q command = %q, want the %s flag", trigger, command, flag)
			}
		}
	}
}

// version is the whole reason the file loads. A value Kiro does not recognize is not a warning, it
// is hooks that never run.
func TestInstallKiroWritesTheV1SchemaVersion(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	if got, _ := readKiroFile(t, path)["version"].(string); got != "v1" {
		t.Fatalf("version = %q, want v1", got)
	}
}

// Every hook is a command hook with an explicit timeout and an explicit enabled flag. The timeout
// matters: Kiro's default is 60 seconds, which is a long time to hold an agent turn for a hook
// that finishes in milliseconds.
func TestInstallKiroWritesTypedCommandHooks(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	timeouts := map[string]int{}
	for _, event := range kiroEvents {
		timeouts[event.trigger] = event.timeout
	}
	for _, hook := range kiroHookObjects(t, path) {
		trigger, _ := hook["trigger"].(string)
		action, _ := hook["action"].(map[string]interface{})
		if got, _ := action["type"].(string); got != "command" {
			t.Errorf("trigger %q action.type = %q, want command", trigger, got)
		}
		if got, _ := hook["enabled"].(bool); !got {
			t.Errorf("trigger %q enabled = %v, want true", trigger, hook["enabled"])
		}
		timeout, ok := hook["timeout"].(float64)
		if !ok {
			t.Fatalf("trigger %q has no timeout: %#v", trigger, hook)
		}
		if int(timeout) != timeouts[trigger] {
			t.Errorf("trigger %q timeout = %d, want %d", trigger, int(timeout), timeouts[trigger])
		}
		// Zero does not mean "the default" on Kiro, it means no timeout at all -- a hook that hung
		// would hang the agent turn with it, indefinitely.
		if timeout == 0 {
			t.Errorf("trigger %q timeout = 0, which disables the timeout entirely", trigger)
		}
	}
}

// Names must be unique: Kiro documents `name` as the hook's identifier and all five live in one
// file.
func TestInstallKiroWritesUniqueHookNames(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	seen := map[string]bool{}
	for _, hook := range kiroHookObjects(t, path) {
		name, _ := hook["name"].(string)
		if name == "" {
			t.Fatalf("hook has no name: %#v", hook)
		}
		if seen[name] {
			t.Fatalf("hook name %q used twice", name)
		}
		seen[name] = true
	}
}

// No matcher, on any trigger. The field is a regex whose subject depends on the trigger -- a tool
// name here, the prompt text there, nothing at all on SessionStart and Stop -- and omitting it
// means always-match, which is what a telemetry hook wants everywhere. The `"*"` that reads as
// "all tools" in Kiro's IDE tool-name field is not a valid regex, so writing it here would at best
// match nothing.
func TestInstallKiroWritesNoMatcher(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	for _, hook := range kiroHookObjects(t, path) {
		if matcher, ok := hook["matcher"]; ok {
			t.Fatalf("trigger %v carries a matcher %q; omitting it is what makes it always-match",
				hook["trigger"], matcher)
		}
	}
	// The reason, stated as a check rather than only as prose: "*" does not compile as a regex, so
	// a future edit that "helpfully" adds it would be adding a hook that cannot fire.
	if _, err := regexp.Compile("*"); err == nil {
		t.Fatal("regexp.Compile(\"*\") succeeded; the reason this test gives for omitting the matcher no longer holds")
	}
}

// Only the two documented top-level keys. Kiro's v1 schema publishes `version` and `hooks` and
// nothing else, and its behavior for an unknown top-level key is not documented -- so the marker
// that other runtimes get goes on each hook's name and description instead.
func TestInstallKiroWritesOnlyDocumentedTopLevelKeys(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	for key := range readKiroFile(t, path) {
		if key != "version" && key != "hooks" {
			t.Fatalf("hook file carries an undocumented top-level key %q", key)
		}
	}
}

// The `agent` action type injects a prompt instead of running a command: it spends model tokens
// and cannot observe anything. Beacon never writes one, and this is the record of why rather than
// an accident of the struct.
func TestInstallKiroWritesNoAgentActionsOrConfirmPrompts(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	for _, forbidden := range []string{`"prompt"`, `"confirm"`, `"confirmCommand"`, `"type": "agent"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("hook file contains %s:\n%s", forbidden, data)
		}
	}
}

// A project-scope hook file gets committed and read by everyone working in the repository, so a
// mode only its author can read would make the hooks stop working for the next person to check it
// out. The same mode is used at user scope so the two scopes do not differ in a way nobody would
// think to look for.
func TestInstallKiroWritesAWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat hook file: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("hook file mode = %v, want 0644", info.Mode().Perm())
	}
}

// The triggers Beacon deliberately does not subscribe to must stay unsubscribed. The file triggers
// would record a second event for a write PostToolUse already covers, in a security log where
// double-counting a file change is worse than the coverage it buys; the task triggers have no
// documented payload; Manual is a person running a hook by hand.
func TestInstallKiroDoesNotSubscribeToDuplicateOrUndocumentedTriggers(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	unwanted := map[string]bool{
		"PostFileCreate": true, "PostFileSave": true, "PostFileDelete": true,
		"PreTaskExec": true, "PostTaskExec": true, "Manual": true,
	}
	for _, hook := range kiroHookObjects(t, path) {
		trigger, _ := hook["trigger"].(string)
		if unwanted[trigger] {
			t.Fatalf("subscribed to %q; see the kiroEvents comment for why it is excluded", trigger)
		}
	}
}

// ---------------------------------------------------------------------------
// Reinstall, uninstall, detection
// ---------------------------------------------------------------------------

// A reinstall replaces the file rather than appending to it. Kiro loads every hook in the file, so
// an install that appended would register each trigger twice and record every event twice.
func TestInstallKiroIsIdempotent(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	installKiroFixture(t, path)
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("reinstall changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if len(kiroHookObjects(t, path)) != len(kiroEvents) {
		t.Fatalf("hook count after reinstall = %d, want %d", len(kiroHookObjects(t, path)), len(kiroEvents))
	}
}

// An install rewrites the whole file, which is what lets it drop a trigger Beacon no longer
// subscribes to. A merge would leave the stale one behind pointing at a subcommand that no longer
// exists.
func TestInstallKiroDropsAStaleBeaconTrigger(t *testing.T) {
	path := kiroUserHooksPath(t)
	stale := `{"version":"v1","hooks":[{"name":"beacon-endpoint-telemetry-postfilesave",` +
		`"trigger":"PostFileSave","action":{"type":"command",` +
		`"command":"'/tmp/beacon-hooks' --platform kiro --log '/tmp/runtime.jsonl' post-tool"}}]}`
	writeKiroFixture(t, path, stale)

	installKiroFixture(t, path)
	for _, hook := range kiroHookObjects(t, path) {
		if trigger, _ := hook["trigger"].(string); trigger == "PostFileSave" {
			t.Fatal("a stale Beacon trigger survived a reinstall")
		}
	}
}

func TestUninstallKiroRemovesTheFile(t *testing.T) {
	path := kiroUserHooksPath(t)
	installKiroFixture(t, path)

	removed, err := removeKiroHooks(path)
	if err != nil {
		t.Fatalf("removeKiroHooks: %v", err)
	}
	if !removed {
		t.Fatal("removeKiroHooks reported no change")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hook file still present: %v", err)
	}
	if isKiroInstalledAt(path) {
		t.Fatal("isKiroInstalledAt = true after uninstall")
	}
}

// Uninstalling twice is not an error, and the second call reports that it changed nothing.
func TestUninstallKiroOnAbsentFileIsANoOp(t *testing.T) {
	path := kiroUserHooksPath(t)
	removed, err := removeKiroHooks(path)
	if err != nil {
		t.Fatalf("removeKiroHooks: %v", err)
	}
	if removed {
		t.Fatal("removeKiroHooks reported a change with no file present")
	}
}

// Detection is by the hook command, not by the file existing. A file left behind by an earlier
// Beacon, or truncated by a failed write, exists and registers nothing -- and reporting that as
// installed is how an operator ends up believing a machine is monitored when it is not.
func TestIsKiroInstalledReadsTheCommandNotTheFile(t *testing.T) {
	path := kiroUserHooksPath(t)

	writeKiroFixture(t, path, `{"version":"v1","hooks":[]}`)
	if isKiroInstalledAt(path) {
		t.Fatal("an empty hooks array reported as installed")
	}
	writeKiroFixture(t, path, `{"version":"v1","hooks":[{"name":"lint","trigger":"PostFileSave",`+
		`"action":{"type":"command","command":"npx eslint --fix"}}]}`)
	if isKiroInstalledAt(path) {
		t.Fatal("somebody else's hook reported as a Beacon install")
	}
	// Removed rather than installed over: the install refuses a file it does not own, which is a
	// separate property with its own test. What is under test here is detection.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	installKiroFixture(t, path)
	if !isKiroInstalledAt(path) {
		t.Fatal("a Beacon install did not report as installed")
	}
}

// ---------------------------------------------------------------------------
// Somebody else's file
// ---------------------------------------------------------------------------

// Beacon owns its filename and nothing beyond it. A file at that name holding a hook Beacon did
// not write is somebody's live registration -- Kiro loads every file in the directory -- so the
// install refuses rather than deleting it, and says where to move it.
func TestInstallKiroRefusesAFileItDoesNotOwn(t *testing.T) {
	path := kiroUserHooksPath(t)
	body := `{"version":"v1","hooks":[{"name":"lint-on-save","trigger":"PostFileSave",` +
		`"action":{"type":"command","command":"npx eslint --fix"}}]}`
	writeKiroFixture(t, path, body)

	err := installKiroHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("install overwrote a hook file Beacon did not write")
	}
	if !strings.Contains(err.Error(), "lint-on-save") {
		t.Errorf("error does not name the hook it refused to replace: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read hook file: %v", readErr)
	}
	if string(data) != body {
		t.Fatalf("the refused file was modified:\n%s", data)
	}
}

// The same check guards uninstall. An uninstall may remove what it added and nothing more.
func TestUninstallKiroLeavesAFileItDoesNotOwn(t *testing.T) {
	path := kiroUserHooksPath(t)
	body := `{"version":"v1","hooks":[{"name":"lint-on-save","trigger":"PostFileSave",` +
		`"action":{"type":"command","command":"npx eslint --fix"}}]}`
	writeKiroFixture(t, path, body)

	if _, err := removeKiroHooks(path); err == nil {
		t.Fatal("uninstall deleted a hook file Beacon did not write")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the refused file was removed: %v", err)
	}
}

// A file Kiro itself could not load has no live hooks to protect, so overwriting it leaves the
// operator with working telemetry rather than an install that refuses over something already
// broken.
func TestInstallKiroOverwritesAnUnparseableFile(t *testing.T) {
	path := kiroUserHooksPath(t)
	writeKiroFixture(t, path, "{ this is not json")

	installKiroFixture(t, path)
	if !isKiroInstalledAt(path) {
		t.Fatal("install did not replace an unparseable hook file")
	}
}

// An empty file is not malformed JSON to a person, and treating it as an error would make an
// install fail on a file somebody created but never wrote.
func TestInstallKiroOverwritesAnEmptyFile(t *testing.T) {
	path := kiroUserHooksPath(t)
	writeKiroFixture(t, path, "")

	installKiroFixture(t, path)
	if !isKiroInstalledAt(path) {
		t.Fatal("install did not fill an empty hook file")
	}
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

// KIRO_HOME exists so a person can keep separate Kiro profiles on one machine, and on a machine
// that sets it ~/.kiro is not where Kiro looks -- so an install that ignored it would write a file
// nothing reads.
func TestKiroUserScopeHonorsKiroHome(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	profile := t.TempDir()
	t.Setenv("KIRO_HOME", profile)

	path, err := kiroHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("kiroHooksPath: %v", err)
	}
	want := filepath.Join(profile, "hooks", kiroHookFileName)
	if path != want {
		t.Fatalf("user scope path = %q, want %q", path, want)
	}
}

// The default scope is the user one, matching every other runtime's installer: an empty Level is
// what the CLI passes when the operator did not ask for a scope.
func TestKiroEmptyLevelIsUserScope(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("KIRO_HOME", "")

	empty, err := kiroHooksPath("")
	if err != nil {
		t.Fatalf("kiroHooksPath(\"\"): %v", err)
	}
	user, err := kiroHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("kiroHooksPath(user): %v", err)
	}
	if empty != user {
		t.Fatalf("empty level = %q, user level = %q; they must agree", empty, user)
	}
}

// Project scope is `<cwd>/.kiro/hooks/`, and it does not consult KIRO_HOME: that variable moves the
// global directory, not the workspace one.
func TestKiroProjectScopeIsRelativeToTheWorkingDirectory(t *testing.T) {
	t.Setenv("KIRO_HOME", t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	path, err := kiroHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("kiroHooksPath: %v", err)
	}
	want := filepath.Join(cwd, ".kiro", "hooks", kiroHookFileName)
	if path != want {
		t.Fatalf("project scope path = %q, want %q", path, want)
	}
}

func TestKiroUnknownLevelIsAnError(t *testing.T) {
	if _, err := kiroHooksPath("machine"); err == nil {
		t.Fatal("kiroHooksPath accepted an unknown level")
	}
}

// Kiro merges hooks across scopes rather than resolving them by precedence, so a user-scope
// install and a project-scope one are both live at once and must not collide on a path. This is
// the opposite of OpenHands, where a project file hides the user one entirely.
func TestKiroScopesAreDistinctPaths(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("KIRO_HOME", "")

	user, err := kiroHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("kiroHooksPath(user): %v", err)
	}
	project, err := kiroHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("kiroHooksPath(project): %v", err)
	}
	if user == project {
		t.Fatalf("both scopes resolved to %q", user)
	}
}

func writeKiroFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write hook fixture: %v", err)
	}
}
