package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/pelletier/go-toml/v2"
)

// Beacon edits a file it does not own, that holds the user's API keys, and that the runtime
// refuses to start without.
//
// Nothing about a bad edit here is visible: Kimi Code exits with a config error the user has to
// connect back to an install they ran days ago, or -- worse -- loads a config that parses and has
// quietly lost a provider. So the properties pinned below are the ones that make a line-based
// edit of somebody's credentials file defensible: the bytes above Beacon's block are untouched,
// the write is verified against a re-parse before it lands, the block is found again by its
// command rather than by a comment that the runtime's own migration would delete, and the one
// document shape that cannot take an appended `[[hooks]]` is refused rather than broken.

const kimiSampleConfig = `# My Kimi Code config.
default_model = "k2"

[providers.kimi]
type = "kimi"
# Rotated 2026-03-01.
api_key = "sk-do-not-touch"

[providers.kimi.env]
KIMI_BASE_URL = "https://api.moonshot.ai/v1"

[models.k2]
provider = "kimi"
model = "kimi-k2"

[identity]
name = "beacon-dev"
`

func kimiUserPath(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv(kimiEnvHome, filepath.Join(t.TempDir(), "kimi-home"))
	path, err := kimiConfigPath(LevelUser)
	if err != nil {
		t.Fatalf("kimiConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return path
}

func writeKimiFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func installKimiFixture(t *testing.T, path string) {
	t.Helper()
	if err := installKimiHooks(path, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", "/etc/beacon/config.json"); err != nil {
		t.Fatalf("installKimiHooks returned error: %v", err)
	}
}

func parseKimiFixture(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	parsed := map[string]interface{}{}
	if err := toml.Unmarshal([]byte(readFile(t, path)), &parsed); err != nil {
		t.Fatalf("the config Beacon wrote is not valid TOML: %v\n---\n%s", err, readFile(t, path))
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// KIMI_CODE_HOME moves the whole Kimi Code data root, so on a machine that sets it ~/.kimi-code is
// not where the runtime looks and an install that ignored it would write into a file nothing
// reads.
func TestKimiConfigPathHonorsKimiCodeHome(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	home := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv(kimiEnvHome, home)

	path, err := kimiConfigPath(LevelUser)
	if err != nil {
		t.Fatalf("kimiConfigPath: %v", err)
	}
	if want := filepath.Join(home, kimiConfigFileName); path != want {
		t.Fatalf("kimiConfigPath = %q, want %q", path, want)
	}
}

func TestKimiConfigPathDefaultsToDotKimiCodeUnderHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv(kimiEnvHome, "")

	path, err := kimiConfigPath(LevelUser)
	if err != nil {
		t.Fatalf("kimiConfigPath: %v", err)
	}
	if want := filepath.Join(home, kimiHomeDirName, kimiConfigFileName); path != want {
		t.Fatalf("kimiConfigPath = %q, want %q", path, want)
	}
}

// Refusing is the point. Kimi Code reads one user-level config file, so a project-scoped install
// that silently fell back to user scope would give an operator a machine-wide install when they
// asked for a repository-scoped one, with nothing to say that is what happened.
func TestKimiProjectScopeIsRefusedWithItsReason(t *testing.T) {
	_, err := kimiConfigPath(LevelProject)
	if err == nil {
		t.Fatal("kimiConfigPath(LevelProject) returned no error; a silent fallback to user scope " +
			"would install machine-wide without saying so")
	}
	if !strings.Contains(err.Error(), "user-level") || !strings.Contains(err.Error(), kimiEnvHome) {
		t.Fatalf("error = %q; it must say why there is no project scope and what to do instead", err)
	}
}

// ---------------------------------------------------------------------------
// The emitted block
// ---------------------------------------------------------------------------

func TestKimiInstallWritesEveryEventItSubscribesTo(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != len(kimiEvents) {
		t.Fatalf("wrote %d hook entries, want %d", len(entries), len(kimiEvents))
	}
	byEvent := map[string]kimiHookEntry{}
	for _, entry := range entries {
		byEvent[entry.Event] = entry
	}
	for _, event := range kimiEvents {
		entry, ok := byEvent[event.name]
		if !ok {
			t.Fatalf("no hook entry for %s", event.name)
		}
		if !strings.HasSuffix(entry.Command, " "+event.subcommand) {
			t.Fatalf("%s command = %q, want it to end in the %s subcommand", event.name, entry.Command, event.subcommand)
		}
		if !strings.Contains(entry.Command, "--platform kimi") {
			t.Fatalf("%s command = %q, want --platform kimi", event.name, entry.Command)
		}
		if entry.Timeout != event.timeout {
			t.Fatalf("%s timeout = %d, want %d", event.name, entry.Timeout, event.timeout)
		}
		// Kimi Code validates the field as an integer in 1..600 and refuses to load the whole
		// config file when it is outside that range -- which would take the user's agent down
		// rather than just this hook.
		if entry.Timeout < 1 || entry.Timeout > 600 {
			t.Fatalf("%s timeout = %d, outside the 1..600 the runtime accepts", event.name, entry.Timeout)
		}
	}
}

// An absent matcher is match-all. It is written as absent rather than as ".*" or "*" because a
// present matcher is compiled as a regular expression and one the runtime cannot compile matches
// nothing at all, silently -- which would be an install that reports success and collects nothing.
func TestKimiInstallOmitsTheMatcher(t *testing.T) {
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	if strings.Contains(readFile(t, path), "matcher") {
		t.Fatalf("the written config names a matcher:\n%s", readFile(t, path))
	}
	for _, entry := range kimiHookEntries(parseKimiFixture(t, path)) {
		if entry.Matcher != "" {
			t.Fatalf("%s matcher = %q, want absent", entry.Event, entry.Matcher)
		}
	}
}

// The runtime's hook schema is strict: an unknown key in a `[[hooks]]` entry fails validation and
// the whole config file stops loading. So the four fields are not a convenience subset, and a
// fifth would break every Kimi Code session on the machine.
func TestKimiHookEntriesCarryOnlyTheFourFieldsTheSchemaAllows(t *testing.T) {
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	var doc struct {
		Hooks []map[string]interface{} `toml:"hooks"`
	}
	if err := toml.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{"event": true, "matcher": true, "command": true, "timeout": true}
	for _, entry := range doc.Hooks {
		for key := range entry {
			if !allowed[key] {
				t.Fatalf("hook entry carries %q; an unrecognized key stops the whole config loading", key)
			}
		}
	}
}

// PostToolUseFailure and PermissionResult share a subcommand with their partner event. The
// mapper tells the pairs apart from `hook_event_name`, so the install must actually register all
// four -- a missing failure hook means every failed tool call goes unrecorded, and a missing
// PermissionResult means Beacon records that an operator was asked and never what they said.
func TestKimiInstallRegistersBothHalvesOfEachPair(t *testing.T) {
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	bySubcommand := map[string][]string{}
	for _, entry := range kimiHookEntries(parseKimiFixture(t, path)) {
		fields := strings.Fields(entry.Command)
		bySubcommand[fields[len(fields)-1]] = append(bySubcommand[fields[len(fields)-1]], entry.Event)
	}
	for subcommand, want := range map[string][]string{
		"post-tool":          {"PostToolUse", "PostToolUseFailure"},
		"permission-request": {"PermissionRequest", "PermissionResult"},
	} {
		got := bySubcommand[subcommand]
		if len(got) != len(want) {
			t.Fatalf("%s is bound to %v, want %v", subcommand, got, want)
		}
	}
}

// SessionHeartbeat is the one event where subscribing would create the behavior rather than
// observe it: Kimi Code starts its sixty-second timer only when a hook is configured for it, so
// Beacon would be spawning a process a minute in every session to write an event that says
// nothing happened.
func TestKimiInstallDoesNotSubscribeToTheHeartbeat(t *testing.T) {
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	for _, entry := range kimiHookEntries(parseKimiFixture(t, path)) {
		if entry.Event == "SessionHeartbeat" {
			t.Fatal("Beacon registered SessionHeartbeat; the runtime runs that timer only when a " +
				"hook asks for it, so this creates a wakeup a minute in every session")
		}
	}
}

// ---------------------------------------------------------------------------
// The user's file
// ---------------------------------------------------------------------------

// The property the whole design exists for. Beacon appends and never re-serializes, so the bytes
// above its block -- the API key, its rotation comment, the quoting style, the ordering -- are
// exactly as the user left them.
func TestKimiInstallLeavesEverythingAboveItsBlockByteForByte(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	updated := readFile(t, path)
	if !strings.HasPrefix(updated, kimiSampleConfig) {
		t.Fatalf("the original text is no longer a prefix of the file:\n---\n%s", updated)
	}
	for _, fragment := range []string{
		`api_key = "sk-do-not-touch"`,
		"# Rotated 2026-03-01.",
		"# My Kimi Code config.",
		`KIMI_BASE_URL = "https://api.moonshot.ai/v1"`,
	} {
		if !strings.Contains(updated, fragment) {
			t.Fatalf("install lost %q from the user's config", fragment)
		}
	}
}

// A config that already has the user's own hooks keeps them, and Beacon's go after.
func TestKimiInstallKeepsTheUsersOwnHooks(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig+`
[[hooks]]
event = "Notification"
matcher = "task\\.completed"
command = "terminal-notifier -title Kimi -message 'Task done'"
`)
	installKimiFixture(t, path)

	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != len(kimiEvents)+1 {
		t.Fatalf("got %d entries, want the user's one plus Beacon's %d", len(entries), len(kimiEvents))
	}
	if entries[0].Event != "Notification" || entries[0].Matcher != `task\.completed` {
		t.Fatalf("the user's hook was not preserved first: %+v", entries[0])
	}
	if !strings.Contains(entries[0].Command, "terminal-notifier") {
		t.Fatalf("the user's hook command changed: %q", entries[0].Command)
	}
}

// Reinstalling must replace Beacon's entries rather than stacking a second copy of them, or every
// event would fire two hooks and write every line twice. The runtime does deduplicate identical
// commands, but a repair that changed the log path would produce two that are not identical.
func TestKimiReinstallReplacesRatherThanStacks(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	if err := installKimiHooks(path, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/other.jsonl", "/etc/beacon/config.json"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}

	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != len(kimiEvents) {
		t.Fatalf("got %d entries after a reinstall, want %d", len(entries), len(kimiEvents))
	}
	for _, entry := range entries {
		if strings.Contains(entry.Command, "runtime.jsonl") {
			t.Fatalf("a hook from the first install survived: %q", entry.Command)
		}
	}
	// The label is written once, not once per install.
	if got := strings.Count(readFile(t, path), kimiManagedComment); got != 1 {
		t.Fatalf("the managed-block comment appears %d times, want 1", got)
	}
}

// Install, uninstall, install must return the file to the same place rather than accumulating
// blank lines or losing a trailing newline each round.
func TestKimiInstallUninstallInstallIsStable(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)

	installKimiFixture(t, path)
	first := readFile(t, path)
	if _, err := removeKimiHooks(path); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	installKimiFixture(t, path)
	if second := readFile(t, path); second != first {
		t.Fatalf("install/uninstall/install is not stable:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// A config whose only hooks were Beacon's comes back to exactly what it was.
func TestKimiUninstallRestoresTheOriginalText(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	removed, err := removeKimiHooks(path)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !removed {
		t.Fatal("uninstall reported nothing was removed")
	}
	if got := readFile(t, path); got != kimiSampleConfig {
		t.Fatalf("uninstall did not restore the original:\n--- want ---\n%s\n--- got ---\n%s", kimiSampleConfig, got)
	}
}

func TestKimiUninstallKeepsTheUsersOwnHooks(t *testing.T) {
	path := kimiUserPath(t)
	userHook := `
[[hooks]]
event = "Notification"
command = "terminal-notifier -title Kimi -message 'Task done'"
`
	writeKimiFixture(t, path, kimiSampleConfig+userHook)
	installKimiFixture(t, path)

	if _, err := removeKimiHooks(path); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != 1 || entries[0].Event != "Notification" {
		t.Fatalf("uninstall left %+v, want only the user's own hook", entries)
	}
}

func TestKimiUninstallIsIdempotent(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	if removed, err := removeKimiHooks(path); err != nil || !removed {
		t.Fatalf("first uninstall: removed=%v err=%v", removed, err)
	}
	if removed, err := removeKimiHooks(path); err != nil || removed {
		t.Fatalf("second uninstall: removed=%v err=%v, want false and no error", removed, err)
	}
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

// The reachable case that makes detection by command necessary rather than tidy: Kimi Code's
// legacy migration from `kimi-cli` merges the old home's settings into this file by serializing
// the merged result, which keeps the `[[hooks]]` entries and drops every comment in the document.
// A marker-based uninstall would silently fail to find its own block on any machine that had been
// through that upgrade.
func TestKimiDetectionSurvivesTheRuntimeRewritingTheFile(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	installKimiFixture(t, path)

	// Reproduce the rewrite: parse, re-serialize, write back. Every comment goes, including
	// Beacon's label; the hooks stay.
	parsed := parseKimiFixture(t, path)
	serialized, err := toml.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeKimiFixture(t, path, string(serialized))
	if strings.Contains(readFile(t, path), kimiManagedComment) {
		t.Fatal("the fixture did not reproduce the rewrite; the comment survived")
	}

	if !isKimiInstalledAt(path) {
		t.Fatal("isKimiInstalledAt = false after the runtime rewrote the file; detection must go " +
			"by the hook command, not by Beacon's comment")
	}
	removed, err := removeKimiHooks(path)
	if err != nil {
		t.Fatalf("uninstall after the rewrite: %v", err)
	}
	if !removed {
		t.Fatal("uninstall found nothing to remove after the runtime rewrote the file")
	}
	if entries := kimiHookEntries(parseKimiFixture(t, path)); len(entries) != 0 {
		t.Fatalf("uninstall left %d entries after the rewrite", len(entries))
	}
}

// A config with a `hooks` array that is not Beacon's is not an install, and a config with no
// hooks at all is not either. The first direction is the costly one: reporting it installed would
// have an operator believe a machine is monitored when it is not.
func TestKimiIsInstalledGoesByTheCommand(t *testing.T) {
	path := kimiUserPath(t)

	writeKimiFixture(t, path, kimiSampleConfig)
	if isKimiInstalledAt(path) {
		t.Fatal("isKimiInstalledAt = true for a config with no hooks")
	}

	writeKimiFixture(t, path, kimiSampleConfig+`
[[hooks]]
event = "PreToolUse"
command = "node ~/.kimi-code/hooks/block-dangerous-bash.mjs"
`)
	if isKimiInstalledAt(path) {
		t.Fatal("isKimiInstalledAt = true for somebody else's hook")
	}

	installKimiFixture(t, path)
	if !isKimiInstalledAt(path) {
		t.Fatal("isKimiInstalledAt = false after an install")
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// TOML does not allow `[[hooks]]` after `hooks = [...]`, so appending would leave a config file
// Kimi Code refuses to start with. That is not a degraded install -- it is the user's agent down
// -- so the install refuses and says what to do.
func TestKimiInstallRefusesAnInlineHooksArray(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, `default_model = "k2"
hooks = [{ event = "Stop", command = "echo done" }]
`)

	err := installKimiHooks(path, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", "")
	if err == nil {
		t.Fatal("install accepted an inline hooks array; the appended [[hooks]] would make the " +
			"config unloadable")
	}
	if !strings.Contains(err.Error(), "inline array") {
		t.Fatalf("error = %q; it must name the shape it is refusing", err)
	}
	if strings.Contains(readFile(t, path), "beacon-hooks") {
		t.Fatal("the refused install still wrote to the file")
	}
}

// A config Kimi Code cannot load is one it is already refusing to start with. Writing into it
// would replace a problem the user can see with one Beacon caused.
func TestKimiInstallRefusesAnUnparsableConfig(t *testing.T) {
	path := kimiUserPath(t)
	broken := "default_model = \nthis is not toml\n"
	writeKimiFixture(t, path, broken)

	err := installKimiHooks(path, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", "")
	if err == nil {
		t.Fatal("install accepted a config that is not valid TOML")
	}
	if got := readFile(t, path); got != broken {
		t.Fatalf("the refused install changed the file:\n%s", got)
	}
}

// The other direction of the same rule. An uninstall does not rewrite a file it cannot parse, and
// it does not fail either: the hooks in it are not running whatever Beacon does.
func TestKimiUninstallLeavesAnUnparsableConfigAlone(t *testing.T) {
	path := kimiUserPath(t)
	broken := "[[hooks]\nevent = \n"
	writeKimiFixture(t, path, broken)

	removed, err := removeKimiHooks(path)
	if err != nil {
		t.Fatalf("uninstall on an unparsable config returned an error: %v", err)
	}
	if removed {
		t.Fatal("uninstall claimed to remove hooks from a config it cannot parse")
	}
	if got := readFile(t, path); got != broken {
		t.Fatalf("uninstall changed an unparsable config:\n%s", got)
	}
}

// The safety net itself. Nothing this package writes reaches the disk without being parsed back
// and compared, so a scanning or quoting mistake fails the operation instead of damaging a
// credentials file.
func TestKimiRewriteVerificationRejectsALostSetting(t *testing.T) {
	path := kimiUserPath(t)
	err := verifyKimiRewrite(path, kimiSampleConfig, strings.Replace(
		kimiSampleConfig, `api_key = "sk-do-not-touch"`, "", 1), nil)
	if err == nil {
		t.Fatal("verifyKimiRewrite accepted an edit that dropped a provider's api_key")
	}
	if !strings.Contains(err.Error(), "providers") {
		t.Fatalf("error = %q; it must name the key that would have changed", err)
	}
}

func TestKimiRewriteVerificationRejectsAnAddedSetting(t *testing.T) {
	path := kimiUserPath(t)
	err := verifyKimiRewrite(path, kimiSampleConfig, kimiSampleConfig+"\ntelemetry = false\n", nil)
	if err == nil {
		t.Fatal("verifyKimiRewrite accepted an edit that added a top-level key")
	}
}

func TestKimiRewriteVerificationRejectsUnparsableOutput(t *testing.T) {
	path := kimiUserPath(t)
	err := verifyKimiRewrite(path, kimiSampleConfig, kimiSampleConfig+"\n[[hooks]\n", nil)
	if err == nil {
		t.Fatal("verifyKimiRewrite accepted output that is not valid TOML")
	}
	if !strings.Contains(err.Error(), "left unchanged") {
		t.Fatalf("error = %q; it must say the file was not written", err)
	}
}

// ---------------------------------------------------------------------------
// Fresh installs and file mode
// ---------------------------------------------------------------------------

// A machine where Kimi Code has never run has no config.toml. Creating one is safe: the runtime's
// legacy migration adopts an existing parseable config by merging into it, so a file with only
// hooks in it does not cost the user their old settings.
func TestKimiInstallCreatesAConfigWhenThereIsNone(t *testing.T) {
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != len(kimiEvents) {
		t.Fatalf("got %d entries in a fresh config, want %d", len(entries), len(kimiEvents))
	}
}

// config.toml is where Kimi Code keeps `api_key` in plain text. A config Beacon brings into
// existence must not be world-readable even though it has nothing secret in it yet, because the
// user's next `kimi` login will put a key in this exact file.
func TestKimiFreshConfigIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := kimiUserPath(t)
	installKimiFixture(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600 for a credentials file Beacon created", info.Mode().Perm())
	}
}

// An existing file keeps the mode the user chose for it. Kimi Code may be launched by a different
// account than the one that ran the install, and tightening somebody's config out from under them
// would break the runtime with no diagnostic.
func TestKimiInstallKeepsAnExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := kimiUserPath(t)
	writeKimiFixture(t, path, kimiSampleConfig)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	installKimiFixture(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %v, want the 0640 the file already had", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// Quoting
// ---------------------------------------------------------------------------

// Kimi Code runs a hook through Node's `spawn(command, [], {shell: true})`, which is cmd.exe on
// Windows -- where a single quote is an ordinary character, not quoting. A single-quoted path
// under `C:\Program Files\` would be looked up with the quotes in it, the spawn would fail, and
// the runtime is fail-open on a hook that cannot start: the install would report success and
// collect nothing, silently.
func TestKimiCommandIsQuotedForTheShellThatWillRunIt(t *testing.T) {
	command := kimiCommandPrefix(`C:\Program Files\Beacon\beacon-hooks.exe`, `C:\ProgramData\Beacon\runtime.jsonl`, "")
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(command, `"C:\Program Files\Beacon\beacon-hooks.exe"`) {
			t.Fatalf("command = %q, want a double-quoted path for cmd.exe", command)
		}
		if strings.Contains(command, "'") {
			t.Fatalf("command = %q; single quotes are literal characters in cmd.exe", command)
		}
	} else if !strings.HasPrefix(command, "'") {
		t.Fatalf("command = %q, want a single-quoted path for /bin/sh", command)
	}
}

// Whichever form was written, the detection side has to recognize it -- otherwise a repair adds a
// second hook beside the first and uninstall leaves both behind.
func TestKimiCommandIsRecognizedInBothQuotingForms(t *testing.T) {
	for name, command := range map[string]string{
		"posix":   `'/opt/beacon/bin/beacon-hooks' --platform kimi --log '/var/log/beacon/runtime.jsonl' pre-tool`,
		"windows": `"C:\Program Files\Beacon\beacon-hooks.exe" --platform kimi --log "C:\ProgramData\Beacon\runtime.jsonl" pre-tool`,
	} {
		t.Run(name, func(t *testing.T) {
			if !isEndpointHookCommand(command, "kimi") {
				t.Fatalf("isEndpointHookCommand(%q) = false", command)
			}
			// And it must not claim another runtime's hook.
			if isEndpointHookCommand(strings.Replace(command, "--platform kimi", "--platform claude", 1), "kimi") {
				t.Fatal("a Claude Code hook was claimed as Kimi Code's")
			}
		})
	}
}

// A data root whose path contains a character TOML would have to escape must still produce a
// config the runtime can load. The command is rendered through the marshaller for exactly this.
func TestKimiCommandWithAwkwardPathStaysValidTOML(t *testing.T) {
	path := kimiUserPath(t)
	awkward := filepath.Join(t.TempDir(), `dir with "quotes" and \backslash`)
	if err := installKimiHooks(path, filepath.Join(awkward, "beacon-hooks"), "/var/log/beacon/runtime.jsonl", ""); err != nil {
		t.Fatalf("install with an awkward path: %v", err)
	}
	entries := kimiHookEntries(parseKimiFixture(t, path))
	if len(entries) != len(kimiEvents) {
		t.Fatalf("got %d entries, want %d", len(entries), len(kimiEvents))
	}
	if !strings.Contains(entries[0].Command, awkward) {
		t.Fatalf("the path did not survive TOML encoding: %q", entries[0].Command)
	}
}

// ---------------------------------------------------------------------------
// Line endings
// ---------------------------------------------------------------------------

// A Windows user's config.toml uses CRLF. Appending LF-terminated lines to it would leave a file
// with mixed endings, which the runtime's own config writer detects and would then propagate.
func TestKimiInstallMatchesTheExistingLineEnding(t *testing.T) {
	path := kimiUserPath(t)
	writeKimiFixture(t, path, strings.ReplaceAll(kimiSampleConfig, "\n", "\r\n"))
	installKimiFixture(t, path)

	updated := readFile(t, path)
	if strings.Count(updated, "\n") != strings.Count(updated, "\r\n") {
		t.Fatalf("install mixed line endings into a CRLF config:\n%q", updated)
	}
	if len(kimiHookEntries(parseKimiFixture(t, path))) != len(kimiEvents) {
		t.Fatal("the CRLF config did not parse back into the expected hooks")
	}
}
