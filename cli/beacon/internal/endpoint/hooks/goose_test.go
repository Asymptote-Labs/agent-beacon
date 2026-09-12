package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// goose's hook loader is silent about a file it does not like, the same way Kiro's, OpenHands' and
// Muse's are, and in several distinct ways: an unparseable hooks.json is skipped with a warn line,
// an unknown event name is ignored, an unsupported action type is ignored, and an invalid matcher
// regex drops just that rule. Nothing reaches the operator. The emitted shape is therefore pinned
// here rather than trusted.

// gooseUserHooksPath points the user scope at a temp home and returns the path Beacon writes.
//
// GOOSE_PATH_ROOT is cleared as well as HOME because gooseUserAgentsDir prefers it, and a developer
// with it set would otherwise have these tests resolve outside the temp directory.
func gooseUserHooksPath(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv(goosePathRootEnv, "")
	path, err := gooseHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("gooseHooksPath: %v", err)
	}
	return path
}

func installGooseFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := installGooseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installGooseHooks returned error: %v", err)
	}
}

// readGooseFile decodes the emitted file into a loose map, so an assertion can ask about a key the
// typed struct does not have. Several tests below are about keys Beacon must *not* write.
func readGooseFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode hooks file: %v", err)
	}
	return document
}

// gooseActions returns every action object the file registers, keyed by event name.
func gooseActions(t *testing.T, path string) map[string][]map[string]interface{} {
	t.Helper()
	document := readGooseFile(t, path)
	events, ok := document["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("hooks file has no hooks object: %#v", document)
	}
	out := map[string][]map[string]interface{}{}
	for event, rawRules := range events {
		rules, ok := rawRules.([]interface{})
		if !ok {
			t.Fatalf("event %q is not an array of rules: %#v", event, rawRules)
		}
		for _, rawRule := range rules {
			rule, ok := rawRule.(map[string]interface{})
			if !ok {
				t.Fatalf("rule under %q is not an object: %#v", event, rawRule)
			}
			rawActions, ok := rule["hooks"].([]interface{})
			if !ok {
				t.Fatalf("rule under %q has no hooks array: %#v", event, rule)
			}
			for _, rawAction := range rawActions {
				action, ok := rawAction.(map[string]interface{})
				if !ok {
					t.Fatalf("action under %q is not an object: %#v", event, rawAction)
				}
				out[event] = append(out[event], action)
			}
		}
	}
	return out
}

func gooseCommandFor(t *testing.T, path, event string) string {
	t.Helper()
	actions := gooseActions(t, path)[event]
	if len(actions) != 1 {
		t.Fatalf("event %q registered %d actions, want exactly 1", event, len(actions))
	}
	command, _ := actions[0]["command"].(string)
	return command
}

// ---------------------------------------------------------------------------
// Fresh install
// ---------------------------------------------------------------------------

// The seven events Beacon subscribes to, each bound to the subcommand that maps it. The event
// names are the wire contract: goose ignores a name it does not recognize, so a typo in one is not
// an error -- it is that event silently never firing.
func TestInstallGooseBindsEveryEvent(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	want := map[string]string{
		"SessionStart":       "session-start",
		"UserPromptSubmit":   "prompt-submit",
		"PreToolUse":         "pre-tool",
		"PostToolUse":        "post-tool",
		"PostToolUseFailure": "post-tool",
		"Stop":               "stop",
		"SessionEnd":         "session-end",
	}
	actions := gooseActions(t, path)
	if len(actions) != len(want) {
		t.Fatalf("file registers %d events, want %d: %v", len(actions), len(want), sortedKeys(actions))
	}
	for event, subcommand := range want {
		command := gooseCommandFor(t, path, event)
		if !strings.HasSuffix(command, " "+subcommand) {
			t.Errorf("%s command = %q, want it to end in %q", event, command, subcommand)
		}
		if !strings.Contains(command, "--platform goose") {
			t.Errorf("%s command = %q, want --platform goose", event, command)
		}
	}
}

// PostToolUse and PostToolUseFailure share the post-tool subcommand, and must still be two
// registrations. goose has no single post-tool event; the mapper tells the two apart by reading
// `event`, so losing either one loses half the tool results -- and a map assignment rather than an
// append is exactly how one would overwrite the other.
func TestInstallGooseRegistersBothPostToolEventsSeparately(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	actions := gooseActions(t, path)
	for _, event := range []string{"PostToolUse", "PostToolUseFailure"} {
		if len(actions[event]) != 1 {
			t.Fatalf("%s registered %d actions, want exactly 1", event, len(actions[event]))
		}
	}
}

// The four duplicate events goose also offers, and PreToolUseResult.
//
// BeforeShellExecution, AfterShellExecution, BeforeReadFile and AfterFileEdit each fire alongside
// PreToolUse or PostToolUse for the *same* call, with the same tool_name and tool_input and -- the
// part that makes them worse than redundant -- no tool_call_id at all. Subscribing would record
// every shell command and every file edit twice, with the second copy uncorrelatable.
func TestInstallGooseDoesNotSubscribeToDuplicateEvents(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	actions := gooseActions(t, path)
	for _, event := range []string{
		"BeforeShellExecution", "AfterShellExecution", "BeforeReadFile", "AfterFileEdit",
		"PreToolUseResult",
	} {
		if _, ok := actions[event]; ok {
			t.Errorf("file subscribes to %s; that event duplicates one Beacon already reads, or "+
				"fires once per call to report an outcome that is almost always \"allow\"", event)
		}
	}
}

// goose runs every action through `sh -c` and reads only `type`, `command`, `timeout` and
// `on_failure`. A key it does not know is ignored rather than rejected, so a stray one would sit in
// an operator's plugin directory meaning nothing.
func TestInstallGooseWritesOnlyTheFieldsGooseReads(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	allowed := map[string]bool{"type": true, "command": true, "timeout": true, "on_failure": true}
	for event, actions := range gooseActions(t, path) {
		for _, action := range actions {
			for key := range action {
				if !allowed[key] {
					t.Errorf("%s action carries key %q, which goose does not read", event, key)
				}
			}
			if got, _ := action["type"].(string); got != "command" {
				t.Errorf("%s action type = %q, want command", event, got)
			}
		}
	}
	// The document's only top-level key. goose reads `hooks` and nothing else.
	document := readGooseFile(t, path)
	if len(document) != 1 {
		t.Fatalf("hooks file has %d top-level keys, want only \"hooks\": %v", len(document), sortedKeys(document))
	}
}

// No matcher, on any event, and the reason is specific rather than tidiness: goose tests the regex
// against matcher_context, which is *not* a tool name on every event. HookContext::with_message
// sets it to the prompt text on UserPromptSubmit, so a matcher that looked like a harmless ".*"
// would be a regex run over every prompt, and any narrower one would silently drop prompts that
// did not match it. An omitted matcher always fires.
func TestInstallGooseWritesNoMatcher(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	events, _ := readGooseFile(t, path)["hooks"].(map[string]interface{})
	for event, rawRules := range events {
		for _, rawRule := range rawRules.([]interface{}) {
			rule := rawRule.(map[string]interface{})
			if _, ok := rule["matcher"]; ok {
				t.Errorf("%s rule carries a matcher; matcher_context is the prompt text on "+
					"UserPromptSubmit, so any matcher there filters prompts", event)
			}
		}
	}
}

// on_failure is honored only for PreToolUse, so it is written only there -- writing it everywhere
// would put a field in the file that goose silently ignores on six of seven events.
//
// Its value is "allow", which is already goose's default. It is stated anyway because what it
// guards against is goose changing that default: a telemetry hook that blocked a tool call because
// it had itself failed would turn an observation problem into an availability problem on the
// operator's machine.
func TestInstallGooseWritesOnFailureAllowOnlyWhereGooseReadsIt(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	for event, actions := range gooseActions(t, path) {
		for _, action := range actions {
			value, present := action["on_failure"]
			if event == "PreToolUse" {
				if got, _ := value.(string); got != "allow" {
					t.Errorf("PreToolUse on_failure = %v, want \"allow\" -- a telemetry hook must "+
						"never block a tool call because it failed", value)
				}
				continue
			}
			if present {
				t.Errorf("%s action carries on_failure=%v; goose reads that field only on "+
					"PreToolUse", event, value)
			}
		}
	}
}

// goose timeouts are seconds, and its own default is 30. Every event carries an explicit value
// because the same `timeout` field means milliseconds on Qwen and the files look alike -- and
// because goose holds the turn for a blocking hook, so the value on Stop is the ceiling on a hung
// turn rather than a budget.
func TestInstallGooseWritesSecondTimeoutsOnEveryEvent(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	want := map[string]float64{
		"SessionStart":       gooseSessionStartTimeoutSeconds,
		"UserPromptSubmit":   goosePromptSubmitTimeoutSeconds,
		"PreToolUse":         gooseToolTimeoutSeconds,
		"PostToolUse":        gooseToolTimeoutSeconds,
		"PostToolUseFailure": gooseToolTimeoutSeconds,
		"Stop":               gooseStopTimeoutSeconds,
		"SessionEnd":         gooseSessionEndTimeoutSeconds,
	}
	for event, actions := range gooseActions(t, path) {
		timeout, ok := actions[0]["timeout"].(float64)
		if !ok {
			t.Fatalf("%s action has no timeout: %#v", event, actions[0])
		}
		if timeout != want[event] {
			t.Errorf("%s timeout = %v, want %v seconds", event, timeout, want[event])
		}
		// A millisecond value mistaken for seconds is the failure this catches: 30000 seconds is
		// over eight hours of held turn on a blocking event.
		if timeout > 60 {
			t.Errorf("%s timeout = %v; goose reads this as seconds, so anything this large is a "+
				"millisecond value in the wrong field", event, timeout)
		}
	}
}

// The file lives inside a plugin directory, and goose names a plugin after that directory.
func TestGooseInstallsAsAPluginGooseDiscovers(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	if filepath.Base(path) != "hooks.json" {
		t.Fatalf("hooks file is %q, want hooks.json", filepath.Base(path))
	}
	hooksDir := filepath.Dir(path)
	if filepath.Base(hooksDir) != "hooks" {
		t.Fatalf("hooks file is in %q, want a \"hooks\" directory", filepath.Base(hooksDir))
	}
	pluginRoot := filepath.Dir(hooksDir)
	if filepath.Base(pluginRoot) != goosePluginDirName {
		t.Fatalf("plugin directory is %q, want %q", filepath.Base(pluginRoot), goosePluginDirName)
	}
	// `.agents/plugins` is the Open Plugins layout goose discovers, not a Beacon invention.
	if got := filepath.Base(filepath.Dir(pluginRoot)); got != "plugins" {
		t.Fatalf("plugin root sits in %q, want \"plugins\"", got)
	}
	if got := filepath.Base(filepath.Dir(filepath.Dir(pluginRoot))); got != ".agents" {
		t.Fatalf("plugins directory sits in %q, want \".agents\"", got)
	}
}

// A project-scope plugin gets committed and read by everyone working in the repository, so a mode
// only its author can read would make the hooks stop working for the next person to check it out.
// The same mode at user scope, so the two do not differ in a way nobody would think to look for.
func TestInstallGooseWritesAWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat hooks file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Fatalf("hooks file mode = %o, want 644", perm)
	}
}

// ---------------------------------------------------------------------------
// Reinstall, uninstall, detection
// ---------------------------------------------------------------------------

func TestInstallGooseIsIdempotent(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	installGooseFixture(t, path)
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("reinstall changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// An install replaces the file wholesale, which is what lets it drop an event Beacon no longer
// subscribes to. A merge would leave the stale entry behind pointing at a subcommand that had been
// removed -- a hook that runs on every turn and fails.
func TestInstallGooseDropsAStaleBeaconEvent(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	stale := gooseHooksFile{Hooks: map[string][]gooseHookRule{
		"Stop": {{Hooks: []gooseHookAction{{
			Type:    "command",
			Command: "/tmp/beacon-hooks --platform goose --log /tmp/runtime.jsonl retired-subcommand",
		}}}},
	}}
	data, err := json.MarshalIndent(stale, "", "  ")
	if err != nil {
		t.Fatalf("marshal stale file: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	installGooseFixture(t, path)
	if command := gooseCommandFor(t, path, "Stop"); strings.Contains(command, "retired-subcommand") {
		t.Fatalf("Stop command = %q, want the stale Beacon entry replaced", command)
	}
}

// The uninstall takes the plugin directory with the file, because a plugin directory with no hooks
// file is a plugin goose still discovers, still records in config.yaml's `plugins` map and still
// lists: an empty registration left behind by an uninstall.
func TestUninstallGooseRemovesTheFileAndThePluginDirectory(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	removed, err := removeGooseHooks(path)
	if err != nil {
		t.Fatalf("removeGooseHooks returned error: %v", err)
	}
	if !removed {
		t.Fatal("removeGooseHooks reported nothing removed")
	}
	pluginRoot := filepath.Dir(filepath.Dir(path))
	if _, err := os.Stat(pluginRoot); !os.IsNotExist(err) {
		t.Fatalf("plugin directory %s survived the uninstall (err=%v)", pluginRoot, err)
	}
	// The plugins directory itself is not Beacon's and must survive.
	if _, err := os.Stat(filepath.Dir(pluginRoot)); err != nil {
		t.Fatalf("plugins directory did not survive the uninstall: %v", err)
	}
}

// Removal is bounded to directories Beacon created and left empty. Anything an operator put in
// them survives, because an uninstall may remove what it added and nothing more.
func TestUninstallGooseLeavesAPluginDirectoryWithOtherContent(t *testing.T) {
	path := gooseUserHooksPath(t)
	installGooseFixture(t, path)

	pluginRoot := filepath.Dir(filepath.Dir(path))
	readme := filepath.Join(pluginRoot, "README.md")
	if err := os.WriteFile(readme, []byte("notes\n"), 0644); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	if _, err := removeGooseHooks(path); err != nil {
		t.Fatalf("removeGooseHooks returned error: %v", err)
	}
	if _, err := os.Stat(readme); err != nil {
		t.Fatalf("a file Beacon did not write was removed with the plugin directory: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hooks file survived the uninstall (err=%v); that is what decides whether goose "+
			"runs anything", err)
	}
}

func TestUninstallGooseOnAbsentFileIsANoOp(t *testing.T) {
	path := gooseUserHooksPath(t)
	removed, err := removeGooseHooks(path)
	if err != nil {
		t.Fatalf("removeGooseHooks returned error: %v", err)
	}
	if removed {
		t.Fatal("removeGooseHooks reported a removal with no file present")
	}
}

// Detection reads the hook command, not the file. A file left behind by an older Beacon, or one
// truncated by a failed write, exists and registers nothing -- and reporting that as installed is
// how an operator ends up believing a machine is monitored when it is not.
func TestIsGooseInstalledReadsTheCommandNotTheFile(t *testing.T) {
	path := gooseUserHooksPath(t)
	if isGooseInstalledAt(path) {
		t.Fatal("reported installed with no file present")
	}

	foreign := gooseHooksFile{Hooks: map[string][]gooseHookRule{
		"SessionStart": {{Hooks: []gooseHookAction{{Type: "command", Command: "/usr/local/bin/audit.sh"}}}},
	}}
	data, err := json.MarshalIndent(foreign, "", "  ")
	if err != nil {
		t.Fatalf("marshal foreign file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	if isGooseInstalledAt(path) {
		t.Fatal("reported installed for a file registering somebody else's hook")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove foreign file: %v", err)
	}
	installGooseFixture(t, path)
	if !isGooseInstalledAt(path) {
		t.Fatal("reported not installed after a successful install")
	}
}

// ---------------------------------------------------------------------------
// Files Beacon does not own
// ---------------------------------------------------------------------------

// The plugin directory name is distinctive, and "unlikely to collide" is not a reason to delete a
// stranger's hooks: whatever is in this file is live for every goose session on the machine.
func TestInstallGooseRefusesAFileItDoesNotOwn(t *testing.T) {
	path := gooseUserHooksPath(t)
	foreign := gooseHooksFile{Hooks: map[string][]gooseHookRule{
		"PreToolUse": {{Hooks: []gooseHookAction{{Type: "command", Command: "${PLUGIN_ROOT}/scripts/policy.sh"}}}},
	}}
	data, err := json.MarshalIndent(foreign, "", "  ")
	if err != nil {
		t.Fatalf("marshal foreign file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	err = installGooseHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("install overwrote a file Beacon did not write")
	}
	if !strings.Contains(err.Error(), "policy.sh") {
		t.Fatalf("error does not name the hook it refused over: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file after refused install: %v", readErr)
	}
	if string(after) != string(data) {
		t.Fatalf("file changed despite the refusal:\n%s", after)
	}
}

func TestUninstallGooseLeavesAFileItDoesNotOwn(t *testing.T) {
	path := gooseUserHooksPath(t)
	foreign := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/bin/notify.sh"}]}]}}`)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, foreign, 0644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	if _, err := removeGooseHooks(path); err == nil {
		t.Fatal("uninstall removed a file Beacon did not write")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file after refused uninstall: %v", err)
	}
	if string(after) != string(foreign) {
		t.Fatalf("file changed despite the refusal:\n%s", after)
	}
}

// A file goose cannot parse is one it is already skipping, so there are no live hooks in it to
// protect. Overwriting leaves the operator with working telemetry rather than an install that
// refuses over a file that was already broken.
func TestInstallGooseOverwritesAnUnparseableOrEmptyFile(t *testing.T) {
	for name, contents := range map[string]string{
		"unparseable": "{not json",
		"empty":       "",
		"whitespace":  "   \n",
	} {
		t.Run(name, func(t *testing.T) {
			path := gooseUserHooksPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatalf("create hooks dir: %v", err)
			}
			if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			installGooseFixture(t, path)
			if !isGooseInstalledAt(path) {
				t.Fatal("install did not replace the file")
			}
		})
	}
}

// An action with no command is ignored by goose rather than rejected, so it is not somebody's live
// hook and must not make the install refuse.
func TestInstallGooseIgnoresACommandlessActionWhenCheckingOwnership(t *testing.T) {
	path := gooseUserHooksPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command"}]}]}}`), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	installGooseFixture(t, path)
	if !isGooseInstalledAt(path) {
		t.Fatal("install did not replace a file whose only action goose would ignore")
	}
}

// ---------------------------------------------------------------------------
// Path resolution
// ---------------------------------------------------------------------------

// GOOSE_PATH_ROOT relocates every goose directory, so on a machine that sets it the default
// location is not where goose looks and an install that ignored it would write a file nothing
// reads.
func TestGooseUserScopeHonorsGoosePathRoot(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	root := t.TempDir()
	t.Setenv(goosePathRootEnv, root)

	path, err := gooseHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("gooseHooksPath: %v", err)
	}
	want := filepath.Join(root, ".agents", "plugins", goosePluginDirName, "hooks", "hooks.json")
	if path != want {
		t.Fatalf("gooseHooksPath = %q, want %q", path, want)
	}
}

// goose requires GOOSE_PATH_ROOT to be absolute and ignores it otherwise, so honoring a relative
// value here would write a file nothing reads -- under the working directory, of all places.
func TestGooseUserScopeIgnoresARelativePathRoot(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(goosePathRootEnv, "relative/goose")

	path, err := gooseHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("gooseHooksPath: %v", err)
	}
	want := filepath.Join(home, ".agents", "plugins", goosePluginDirName, "hooks", "hooks.json")
	if path != want {
		t.Fatalf("gooseHooksPath = %q, want the home default %q", path, want)
	}
}

func TestGooseEmptyLevelIsUserScope(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv(goosePathRootEnv, "")

	empty, err := gooseHooksPath("")
	if err != nil {
		t.Fatalf("gooseHooksPath(\"\"): %v", err)
	}
	user, err := gooseHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("gooseHooksPath(LevelUser): %v", err)
	}
	if empty != user {
		t.Fatalf("empty level = %q, user level = %q; they must be the same scope", empty, user)
	}
}

// Project scope is always `<cwd>/.agents/plugins`, and deliberately does not consult
// GOOSE_PATH_ROOT: goose's project lookup joins the project root directly.
func TestGooseProjectScopeIgnoresThePathRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Setenv(goosePathRootEnv, t.TempDir())

	path, err := gooseHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("gooseHooksPath: %v", err)
	}
	want := filepath.Join(cwd, ".agents", "plugins", goosePluginDirName, "hooks", "hooks.json")
	if path != want {
		t.Fatalf("gooseHooksPath = %q, want %q", path, want)
	}
}

// Both scopes write the same plugin directory name deliberately. goose deduplicates plugins by
// name with project scope first, so matching names means one registration -- and one copy of every
// event -- for an operator who installed at both.
func TestGooseScopesShareOnePluginName(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv(goosePathRootEnv, "")

	user, err := gooseHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("gooseHooksPath(user): %v", err)
	}
	project, err := gooseHooksPath(LevelProject)
	if err != nil {
		t.Fatalf("gooseHooksPath(project): %v", err)
	}
	pluginName := func(path string) string { return filepath.Base(filepath.Dir(filepath.Dir(path))) }
	if pluginName(user) != pluginName(project) {
		t.Fatalf("user plugin is %q and project plugin is %q; goose keys deduplication on the "+
			"name, so differing names would run both and double every event",
			pluginName(user), pluginName(project))
	}
}

func TestGooseUnknownLevelIsAnError(t *testing.T) {
	if _, err := gooseHooksPath("cluster"); err == nil {
		t.Fatal("gooseHooksPath accepted an unknown level")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
