package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// OpenHands is silent about a hooks.json it does not like: a file that fails validation is dropped
// with a log line the user never sees, and the agent goes on working with no hooks at all. Nothing
// in the runtime tells Beacon that an install stopped working, so these tests are what would.

// openHandsUserHooksPath points the user scope at a temp home and returns the path Beacon writes.
// OH_PERSISTENCE_DIR is cleared as well as HOME because openHandsConfigDir prefers it, and a
// developer with it set would otherwise have these tests resolve outside the temp directory.
func openHandsUserHooksPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("OH_PERSISTENCE_DIR", "")
	path, err := openHandsHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("openHandsHooksPath: %v", err)
	}
	return path
}

func writeOpenHandsFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write hooks fixture: %v", err)
	}
}

func readOpenHandsDocument(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode hooks.json: %v", err)
	}
	return document
}

// openHandsCommands returns every hook command in the file, keyed by the event it is registered
// under, reading the document exactly as OpenHands does -- unwrapping a legacy wrapper first.
func openHandsCommands(t *testing.T, path string) map[string][]string {
	t.Helper()
	document := readOpenHandsDocument(t, path)
	if wrapper, ok := document[openHandsWrapperKey]; ok {
		document = map[string]json.RawMessage{}
		if err := json.Unmarshal(wrapper, &document); err != nil {
			t.Fatalf("decode hooks wrapper: %v", err)
		}
	}
	out := map[string][]string{}
	for key, raw := range document {
		var groups []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatalf("decode hook event %q: %v", key, err)
		}
		for _, group := range groups {
			for _, hook := range group.Hooks {
				out[key] = append(out[key], hook.Command)
			}
		}
	}
	return out
}

func installOpenHandsFixture(t *testing.T, path string) {
	t.Helper()
	if err := installOpenHandsHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err != nil {
		t.Fatalf("installOpenHandsHooks returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fresh install
// ---------------------------------------------------------------------------

// All six events OpenHands exposes get a hook, under the snake_case names its own writer emits.
// The event names are the wire contract: a typo in one is not an error, it is that event silently
// never firing.
func TestInstallOpenHandsBindsEveryEvent(t *testing.T) {
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)

	commands := openHandsCommands(t, path)
	wantSubcommands := map[string]string{
		"session_start":      "session-start",
		"user_prompt_submit": "prompt-submit",
		"pre_tool_use":       "pre-tool",
		"post_tool_use":      "post-tool",
		"stop":               "stop",
		"session_end":        "session-end",
	}
	if len(commands) != len(wantSubcommands) {
		t.Fatalf("registered events = %v, want exactly %d", commands, len(wantSubcommands))
	}
	for event, subcommand := range wantSubcommands {
		got := commands[event]
		if len(got) != 1 {
			t.Fatalf("event %q has %d hooks, want exactly 1: %v", event, len(got), got)
		}
		if !strings.HasSuffix(got[0], " "+subcommand) {
			t.Errorf("event %q command = %q, want it to end in the %q subcommand", event, got[0], subcommand)
		}
		if !strings.Contains(got[0], "--platform openhands") {
			t.Errorf("event %q command = %q, want --platform openhands", event, got[0])
		}
		// The endpoint settings ride as flags rather than as an inline VAR=value prefix, so the
		// hook reads the right log and config wherever the runtime is running.
		for _, flag := range []string{"--log", "--config"} {
			if !strings.Contains(got[0], flag) {
				t.Errorf("event %q command = %q, want the %s flag", event, got[0], flag)
			}
		}
	}
}

// Every hook Beacon writes is a command hook with an explicit timeout and the marker name. The
// timeout matters: OpenHands defaults to 60 seconds, which is a long time to hold an agent turn
// for a hook that finishes in milliseconds.
func TestInstallOpenHandsWritesTypedCommandHooks(t *testing.T) {
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)

	document := readOpenHandsDocument(t, path)
	for _, event := range openHandsEvents {
		var groups []struct {
			Matcher string             `json:"matcher"`
			Hooks   []openHandsHookRef `json:"hooks"`
		}
		if err := json.Unmarshal(document[event.snakeKey], &groups); err != nil {
			t.Fatalf("decode %q: %v", event.snakeKey, err)
		}
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %q shape = %#v, want one group with one hook", event.snakeKey, groups)
		}
		if groups[0].Matcher != openHandsAllToolsMatcher {
			t.Errorf("event %q matcher = %q, want %q", event.snakeKey, groups[0].Matcher, openHandsAllToolsMatcher)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" {
			t.Errorf("event %q type = %q, want command", event.snakeKey, hook.Type)
		}
		if hook.Name != openHandsHookName {
			t.Errorf("event %q name = %q, want %q", event.snakeKey, hook.Name, openHandsHookName)
		}
		if hook.Timeout != event.timeout {
			t.Errorf("event %q timeout = %d, want %d", event.snakeKey, hook.Timeout, event.timeout)
		}
	}
}

// An async hook is fire-and-forget with its stdout discarded, which would make the policy seam's
// deny unreachable on the one event that can block. Beacon never writes the key, and this is the
// record of why rather than an accident of the struct.
func TestInstallOpenHandsDoesNotWriteAsyncHooks(t *testing.T) {
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	if strings.Contains(string(data), "\"async\"") {
		t.Fatalf("hooks.json declares async hooks:\n%s", data)
	}
}

// hooks.json is a project file that gets committed and read by everyone working in the repository.
// A mode only its author can read would make the hooks stop working for the next checkout.
func TestInstallOpenHandsWritesAWorldReadableFile(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat hooks.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0044 == 0 {
		t.Fatalf("hooks.json mode = %v, want it readable by group and other", perm)
	}
}

// ---------------------------------------------------------------------------
// Merging
// ---------------------------------------------------------------------------

// hooks.json is where a user keeps their own hooks, so Beacon is a guest in it. An install that
// replaced the file would silently end whatever quality gate or block rule was registered there.
func TestInstallOpenHandsPreservesExistingHooks(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "stop": [
    {"matcher": "*", "hooks": [{"command": ".openhands/hooks/on_stop.sh", "timeout": 120}]}
  ],
  "pre_tool_use": [
    {"matcher": "terminal", "hooks": [{"command": ".openhands/hooks/block_dangerous.sh", "timeout": 10}]}
  ]
}`)
	installOpenHandsFixture(t, path)

	commands := openHandsCommands(t, path)
	if !openHandsHasCommand(commands["stop"], ".openhands/hooks/on_stop.sh") {
		t.Fatalf("stop hooks = %v, want the user's on_stop.sh preserved", commands["stop"])
	}
	if !openHandsHasCommand(commands["pre_tool_use"], ".openhands/hooks/block_dangerous.sh") {
		t.Fatalf("pre_tool_use hooks = %v, want the user's block_dangerous.sh preserved", commands["pre_tool_use"])
	}
	for _, event := range []string{"stop", "pre_tool_use"} {
		if !openHandsHasBeaconCommand(commands[event]) {
			t.Fatalf("%s hooks = %v, want Beacon's hook alongside the user's", event, commands[event])
		}
	}
	// The user's own timeout and matcher survive: Beacon appends a group rather than rewriting one.
	document := readOpenHandsDocument(t, path)
	if !strings.Contains(string(document["pre_tool_use"]), `"matcher": "terminal"`) {
		t.Fatalf("pre_tool_use = %s, want the user's terminal matcher untouched", document["pre_tool_use"])
	}
}

// Re-running the install replaces Beacon's own hooks rather than adding a second copy, so a repair
// does not end with the binary firing twice per event.
func TestInstallOpenHandsIsIdempotent(t *testing.T) {
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)
	installOpenHandsFixture(t, path)
	installOpenHandsFixture(t, path)

	for event, commands := range openHandsCommands(t, path) {
		if len(commands) != 1 {
			t.Fatalf("event %q has %d hooks after three installs, want 1: %v", event, len(commands), commands)
		}
	}
}

// The loader unwraps `{"hooks": {...}}` by replacing the whole document with what is under that
// key. Adding a top-level event beside the wrapper does not merge -- it discards Beacon's hooks
// with a log line nobody sees -- so Beacon writes inside the wrapper when it finds one.
func TestInstallOpenHandsWritesInsideTheLegacyWrapper(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "terminal", "hooks": [{"command": ".openhands/hooks/block_dangerous.sh"}]}
    ]
  }
}`)
	installOpenHandsFixture(t, path)

	document := readOpenHandsDocument(t, path)
	if len(document) != 1 {
		t.Fatalf("document keys = %v, want only the hooks wrapper", document)
	}
	if _, ok := document[openHandsWrapperKey]; !ok {
		t.Fatalf("document = %v, want the wrapper preserved", document)
	}
	commands := openHandsCommands(t, path)
	if !openHandsHasBeaconCommand(commands["PreToolUse"]) {
		t.Fatalf("PreToolUse hooks = %v, want Beacon's hook inside the wrapper", commands["PreToolUse"])
	}
	if !openHandsHasCommand(commands["PreToolUse"], ".openhands/hooks/block_dangerous.sh") {
		t.Fatalf("PreToolUse hooks = %v, want the user's hook preserved", commands["PreToolUse"])
	}
}

// Providing both spellings of one event is a hard error that takes the entire file down, not just
// that event. Beacon writes under whichever spelling the file already uses and never introduces a
// second one -- which is why a Claude Code-shaped file keeps its PascalCase names.
func TestInstallOpenHandsFollowsTheExistingEventSpelling(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "PreToolUse": [
    {"matcher": "terminal", "hooks": [{"command": ".openhands/hooks/block_dangerous.sh"}]}
  ],
  "SessionStart": []
}`)
	installOpenHandsFixture(t, path)

	document := readOpenHandsDocument(t, path)
	for _, pascal := range []string{"PreToolUse", "SessionStart"} {
		if _, ok := document[pascal]; !ok {
			t.Fatalf("document = %v, want %q kept", openHandsKeys(document), pascal)
		}
	}
	for _, snake := range []string{"pre_tool_use", "session_start"} {
		if _, ok := document[snake]; ok {
			t.Fatalf("document = %v, want no second spelling %q", openHandsKeys(document), snake)
		}
	}
	// Events the file did not already name get the canonical snake_case spelling.
	if _, ok := document["post_tool_use"]; !ok {
		t.Fatalf("document = %v, want post_tool_use under the canonical name", openHandsKeys(document))
	}
	commands := openHandsCommands(t, path)
	if !openHandsHasBeaconCommand(commands["PreToolUse"]) {
		t.Fatalf("PreToolUse hooks = %v, want Beacon's hook under the existing spelling", commands["PreToolUse"])
	}
}

// ---------------------------------------------------------------------------
// Refusing a file the runtime would reject
// ---------------------------------------------------------------------------

// A file naming one event twice already fails to load, taking every hook in it down with it.
// Installing into it would report success while the runtime went on loading nothing at all.
func TestInstallOpenHandsRefusesDuplicateEventSpellings(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "PreToolUse": [{"matcher": "*", "hooks": [{"command": "a.sh"}]}],
  "pre_tool_use": [{"matcher": "*", "hooks": [{"command": "b.sh"}]}]
}`)

	err := installOpenHandsHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("installOpenHandsHooks succeeded on a file OpenHands rejects")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Fatalf("error = %v, want it to name the duplicated event", err)
	}
	// The file is left exactly as it was: Beacon cannot fix it and must not half-rewrite it.
	commands := openHandsCommands(t, path)
	if openHandsHasBeaconCommand(commands["PreToolUse"]) || openHandsHasBeaconCommand(commands["pre_tool_use"]) {
		t.Fatalf("hooks were written into a rejected file: %v", commands)
	}
}

// HookConfig forbids extra fields, so one unrecognized top-level key fails validation and the whole
// file loads as nothing.
func TestInstallOpenHandsRefusesUnknownEventKeys(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "stop": [{"matcher": "*", "hooks": [{"command": "a.sh"}]}],
  "PreCompact": [{"matcher": "*", "hooks": [{"command": "b.sh"}]}]
}`)

	err := installOpenHandsHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json")
	if err == nil {
		t.Fatal("installOpenHandsHooks succeeded on a file OpenHands rejects")
	}
	if !strings.Contains(err.Error(), "PreCompact") {
		t.Fatalf("error = %v, want it to name the unaccepted event", err)
	}
}

func TestInstallOpenHandsRefusesMalformedJSON(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{"stop": [`)

	if err := installOpenHandsHooks(path, "/tmp/beacon-hooks", "/tmp/runtime.jsonl", "/tmp/config.json"); err == nil {
		t.Fatal("installOpenHandsHooks succeeded on malformed JSON")
	}
}

// An empty file is not malformed JSON to a person, and failing on one would make install fail on a
// hooks.json somebody had created but not written.
func TestInstallOpenHandsTreatsAnEmptyFileAsNoHooks(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, "   \n")
	installOpenHandsFixture(t, path)

	if len(openHandsCommands(t, path)) != len(openHandsEvents) {
		t.Fatalf("commands = %v, want every event registered", openHandsCommands(t, path))
	}
}

// ---------------------------------------------------------------------------
// Status and uninstall
// ---------------------------------------------------------------------------

func TestOpenHandsInstalledDetectionMatchesTheCommand(t *testing.T) {
	path := openHandsUserHooksPath(t)
	if isOpenHandsInstalledAt(path) {
		t.Fatal("isOpenHandsInstalledAt = true before any install")
	}
	installOpenHandsFixture(t, path)
	if !isOpenHandsInstalledAt(path) {
		t.Fatal("isOpenHandsInstalledAt = false after install")
	}

	// A user's own hook is not Beacon's, however it is named.
	other := filepath.Join(t.TempDir(), "hooks.json")
	writeOpenHandsFixture(t, other, `{
  "stop": [{"matcher": "*", "hooks": [{"name": "beacon-endpoint-telemetry", "command": "./my_stop.sh"}]}]
}`)
	if isOpenHandsInstalledAt(other) {
		t.Fatal("isOpenHandsInstalledAt = true for a hook that only borrowed the marker name")
	}
}

// Uninstall removes what Beacon added and nothing more. A file that still holds another tool's
// hooks is rewritten without Beacon's rather than removed.
func TestUninstallOpenHandsLeavesOtherHooksInPlace(t *testing.T) {
	path := openHandsUserHooksPath(t)
	writeOpenHandsFixture(t, path, `{
  "stop": [{"matcher": "*", "hooks": [{"command": ".openhands/hooks/on_stop.sh", "timeout": 120}]}]
}`)
	installOpenHandsFixture(t, path)

	changed, err := removeOpenHandsHooks(path)
	if err != nil {
		t.Fatalf("removeOpenHandsHooks: %v", err)
	}
	if !changed {
		t.Fatal("removeOpenHandsHooks reported no change after an install")
	}
	if isOpenHandsInstalledAt(path) {
		t.Fatal("Beacon hooks survived the uninstall")
	}
	commands := openHandsCommands(t, path)
	if !openHandsHasCommand(commands["stop"], ".openhands/hooks/on_stop.sh") {
		t.Fatalf("stop hooks = %v, want the user's hook left in place", commands["stop"])
	}
	// The events Beacon added and nobody else used are gone rather than left as empty entries.
	if _, ok := commands["pre_tool_use"]; ok {
		t.Fatalf("commands = %v, want the events Beacon added removed entirely", commands)
	}
}

// An uninstall that emptied the file removes it, so nothing is left behind in somebody's
// repository. A file Beacon never touched is left alone and reported as no change.
func TestUninstallOpenHandsRemovesAFileItEmptied(t *testing.T) {
	path := openHandsUserHooksPath(t)
	installOpenHandsFixture(t, path)

	changed, err := removeOpenHandsHooks(path)
	if err != nil {
		t.Fatalf("removeOpenHandsHooks: %v", err)
	}
	if !changed {
		t.Fatal("removeOpenHandsHooks reported no change after an install")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hooks.json still exists after removing the only hooks in it: %v", err)
	}
}

func TestUninstallOpenHandsIsANoOpWhenNothingWasInstalled(t *testing.T) {
	path := openHandsUserHooksPath(t)

	changed, err := removeOpenHandsHooks(path)
	if err != nil {
		t.Fatalf("removeOpenHandsHooks on a missing file: %v", err)
	}
	if changed {
		t.Fatal("removeOpenHandsHooks reported a change with no file present")
	}

	writeOpenHandsFixture(t, path, `{"stop": [{"matcher": "*", "hooks": [{"command": "./mine.sh"}]}]}`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	changed, err = removeOpenHandsHooks(path)
	if err != nil {
		t.Fatalf("removeOpenHandsHooks: %v", err)
	}
	if changed {
		t.Fatal("removeOpenHandsHooks reported a change for a file with no Beacon hooks")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("file was rewritten with no Beacon hooks to remove:\n%s\n%s", before, after)
	}
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

// Both scopes are real, which is the opposite of the Muse and Hermes situation where the project
// scope does nothing at all. Project scope is the documented location every host reads; user scope
// is the SDK loader's second search location.
func TestOpenHandsConfigDirResolvesBothScopes(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("OH_PERSISTENCE_DIR", "")

	userDir, err := openHandsConfigDir(LevelUser)
	if err != nil {
		t.Fatalf("openHandsConfigDir(user): %v", err)
	}
	if want := filepath.Join(home, ".openhands"); userDir != want {
		t.Fatalf("user dir = %q, want %q", userDir, want)
	}
	// The empty level is the same as user scope, which is what an unset --level resolves to.
	defaultDir, err := openHandsConfigDir("")
	if err != nil {
		t.Fatalf("openHandsConfigDir(\"\"): %v", err)
	}
	if defaultDir != userDir {
		t.Fatalf("default dir = %q, want the user dir %q", defaultDir, userDir)
	}

	projectDir, err := openHandsConfigDir(LevelProject)
	if err != nil {
		t.Fatalf("openHandsConfigDir(project): %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if want := filepath.Join(cwd, ".openhands"); projectDir != want {
		t.Fatalf("project dir = %q, want %q", projectDir, want)
	}

	if _, err := openHandsConfigDir("machine"); err == nil {
		t.Fatal("openHandsConfigDir accepted an unknown level")
	}
}

// Sandboxed and containerized setups redirect OpenHands state onto a volume with
// OH_PERSISTENCE_DIR, and on a machine that sets it ~/.openhands is not where OpenHands looks. An
// install that ignored it would write a file the runtime never reads.
func TestOpenHandsUserScopeHonorsThePersistenceDirEnv(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	persistence := t.TempDir()
	t.Setenv("OH_PERSISTENCE_DIR", persistence)

	path, err := openHandsHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("openHandsHooksPath: %v", err)
	}
	if want := filepath.Join(persistence, openHandsHookFileName); path != want {
		t.Fatalf("hooks path = %q, want %q", path, want)
	}
}

func openHandsHasCommand(commands []string, want string) bool {
	for _, command := range commands {
		if strings.Contains(command, want) {
			return true
		}
	}
	return false
}

func openHandsHasBeaconCommand(commands []string) bool {
	for _, command := range commands {
		if isEndpointHookCommand(command, "openhands") {
			return true
		}
	}
	return false
}

func openHandsKeys(document map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	return keys
}
