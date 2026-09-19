package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"gopkg.in/yaml.v3"
)

// The DeepSeek Harness install is two files and neither of them announces a problem.
//
// A hooks file the bridge cannot load produces a warning in the runtime's log and no registered
// hooks; a patch file that does not mount the bridge produces nothing at all. Neither reaches an
// operator, so the emitted shapes are pinned here rather than trusted -- and so is the property
// that makes editing the patch file defensible at all, that a YAML round trip preserves what the
// user wrote.

// dshUserPaths points the user scope at a temp Harness home and returns both paths Beacon writes.
//
// DSH_HOME is set rather than cleared, unlike the Kiro fixture: it is the documented way to move
// the Harness home, and setting it is also what proves the installer reads it.
func dshUserPaths(t *testing.T) (hooksPath, patchPath string) {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	t.Setenv("DSH_HOME", filepath.Join(t.TempDir(), "dsh-home"))
	hooksPath, err := dshHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("dshHooksPath: %v", err)
	}
	return hooksPath, dshPatchPathFor(hooksPath)
}

func installDshFixture(t *testing.T, hooksPath string) {
	t.Helper()
	if err := installDshHooks(hooksPath, "/opt/beacon/bin/beacon-hooks", "/var/log/beacon/runtime.jsonl", "/etc/beacon/config.json"); err != nil {
		t.Fatalf("installDshHooks returned error: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// DSH_HOME moves the Harness home, and on a machine that sets it ~/.dsh is not where dsh looks.
func TestDshHooksPathHonorsDshHome(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	home := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("DSH_HOME", home)

	path, err := dshHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("dshHooksPath: %v", err)
	}
	if want := filepath.Join(home, dshHookFileName); path != want {
		t.Fatalf("dshHooksPath = %q, want %q", path, want)
	}
	if want := filepath.Join(home, dshPatchFileName); dshPatchPathFor(path) != want {
		t.Fatalf("patch path = %q, want %q", dshPatchPathFor(path), want)
	}
}

func TestDshHooksPathDefaultsToDotDshUnderHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("DSH_HOME", "")

	path, err := dshHooksPath(LevelUser)
	if err != nil {
		t.Fatalf("dshHooksPath: %v", err)
	}
	if want := filepath.Join(home, ".dsh", dshHookFileName); path != want {
		t.Fatalf("dshHooksPath = %q, want %q", path, want)
	}
}

// Project scope is refused rather than silently redirected to user scope. An operator asking for a
// repository-scoped install must not get a machine-wide one without being told.
func TestDshProjectScopeIsRefusedWithItsReason(t *testing.T) {
	_, err := dshHooksPath(LevelProject)
	if err == nil {
		t.Fatal("dshHooksPath(project) returned no error; dsh has no project-scoped hook config")
	}
	if !strings.Contains(err.Error(), "user scope") {
		t.Fatalf("error %q does not tell the operator what to do instead", err)
	}
}

// ---------------------------------------------------------------------------
// The hooks file
// ---------------------------------------------------------------------------

// The emitted hooks document, pinned field by field. The bridge parses this file once at startup
// and registers nothing at all if it cannot -- with a warning in a log, not an error an operator
// sees.
func TestDshHooksFileShape(t *testing.T) {
	hooksPath, _ := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	var file map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, hooksPath)), &file); err != nil {
		t.Fatalf("emitted hooks file is not valid JSON: %v", err)
	}

	wantEvents := map[string]string{
		"SessionStart":     "session-start",
		"UserPromptSubmit": "prompt-submit",
		"PreToolUse":       "pre-tool",
		"PostToolUse":      "post-tool",
		"Stop":             "stop",
		"SubagentStart":    "subagent-start",
		"SubagentStop":     "subagent-stop",
	}
	if len(file) != len(wantEvents) {
		t.Fatalf("hooks file has %d events, want %d -- the bridge supports exactly seven",
			len(file), len(wantEvents))
	}
	for event, subcommand := range wantEvents {
		groups, ok := file[event]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s = %#v, want exactly one group with one hook", event, groups)
		}
		// A matcher is absent by construction: absent means match-all in this dialect, and the
		// bridge validates a written one at parse time and rejects the whole config on a bad one.
		if groups[0].Matcher != nil {
			t.Fatalf("%s carries a matcher %q; absent is the match-all form that needs no validating",
				event, *groups[0].Matcher)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" {
			t.Fatalf("%s hook type = %q; the bridge runs only command handlers", event, hook.Type)
		}
		if !strings.HasSuffix(hook.Command, " "+subcommand) {
			t.Fatalf("%s command = %q, want it to end in %q", event, hook.Command, subcommand)
		}
		if !strings.Contains(hook.Command, "--platform dsh") {
			t.Fatalf("%s command = %q, want --platform dsh", event, hook.Command)
		}
		// Seconds, not milliseconds. The bridge reads `timeout` as timeoutSec and applies 600
		// seconds when it is absent -- ten minutes of held agent turn for a hook that finishes in
		// milliseconds.
		if hook.Timeout <= 0 || hook.Timeout > 60 {
			t.Fatalf("%s timeout = %d; these are seconds and are ceilings on a hang", event, hook.Timeout)
		}
	}
}

// Beacon owns the filename, so it refuses rather than deleting a stranger's registrations.
func TestDshInstallRefusesAHooksFileBeaconDidNotWrite(t *testing.T) {
	hooksPath, _ := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := `{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/bin/audit-tool"}]}]}`
	if err := os.WriteFile(hooksPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := installDshHooks(hooksPath, "/opt/beacon/bin/beacon-hooks", "/log", "/config")
	if err == nil {
		t.Fatal("install overwrote a hooks file Beacon did not write")
	}
	if !strings.Contains(err.Error(), "audit-tool") {
		t.Fatalf("error %q does not name the hook it refused to replace", err)
	}
	if got := readFile(t, hooksPath); got != theirs {
		t.Fatalf("the file was modified anyway: %q", got)
	}
}

// A reinstall replaces the hooks file wholesale, which is what lets Beacon drop an event it no
// longer subscribes to. A merge would leave one behind pointing at a subcommand that was removed.
func TestDshReinstallReplacesTheHooksFileWholesale(t *testing.T) {
	hooksPath, _ := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	file, err := readDshHooks(hooksPath)
	if err != nil {
		t.Fatalf("readDshHooks: %v", err)
	}
	// A stale Beacon event from an imagined earlier version.
	file["PreCompact"] = []dshHookGroup{{Hooks: []dshHookRef{{
		Type:    "command",
		// Spelled the way the installer spells one: isEndpointHookCommand recognizes a Beacon
		// command by its binary AND its endpoint settings, so a fixture missing --log and --config
		// is not one Beacon would be asked to replace.
		Command: "'/opt/beacon/bin/beacon-hooks' --platform dsh --log '/var/log/beacon/runtime.jsonl' --config '/etc/beacon/config.json' pre-compact",
	}}}}
	data, _ := json.MarshalIndent(file, "", "  ")
	if err := os.WriteFile(hooksPath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	installDshFixture(t, hooksPath)
	if strings.Contains(readFile(t, hooksPath), "PreCompact") {
		t.Fatal("a reinstall left a stale Beacon event behind")
	}
}

// ---------------------------------------------------------------------------
// The patch file
// ---------------------------------------------------------------------------

// The mount, pinned. Without it the hooks file is a file nothing reads.
func TestDshInstallMountsTheBridge(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	var elements []struct {
		Insert []struct {
			ID     string `yaml:"id"`
			Name   string `yaml:"name"`
			Config struct {
				ConfigPath string `yaml:"configPath"`
			} `yaml:"config"`
		} `yaml:"insert"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, patchPath)), &elements); err != nil {
		t.Fatalf("emitted patch file is not valid YAML: %v", err)
	}
	if len(elements) != 1 || len(elements[0].Insert) != 1 {
		t.Fatalf("patch = %#v, want one element inserting one row", elements)
	}
	row := elements[0].Insert[0]
	if row.ID != dshPatchEntryID {
		t.Fatalf("row id = %q, want %q", row.ID, dshPatchEntryID)
	}
	if row.Name != dshBridgePackage {
		t.Fatalf("row name = %q, want the bridge package", row.Name)
	}
	// Absolute, and this is required rather than tidy: the bridge resolves a relative config path
	// against the directory the process was launched from.
	if !filepath.IsAbs(row.Config.ConfigPath) {
		t.Fatalf("configPath = %q, want an absolute path", row.Config.ConfigPath)
	}
	if row.Config.ConfigPath != hooksPath {
		t.Fatalf("configPath = %q, want the hooks file Beacon wrote at %q", row.Config.ConfigPath, hooksPath)
	}
}

// The property that makes editing a live user file defensible. It is a property of yaml.v3 rather
// than of this code, which is exactly why it is pinned: a library change would take a user's boot
// configuration with it, silently.
func TestDshInstallPreservesCommentsQuotingAndJsTags(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := `# Our team's overlay. Do not remove the webhook row.
- insert:
    - id: github-webhook-server
      name: '@deepseek-ai/dsh-host-webserver'
      config:
        host: '127.0.0.1'
        port: !!js Number(process.env.DSH_WEBHOOK_PORT ?? 3081)
- id: ui-schedule
  disabled: false # re-enabled for the on-call rota
`
	if err := os.WriteFile(patchPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	installDshFixture(t, hooksPath)
	got := readFile(t, patchPath)

	for _, want := range []string{
		"# Our team's overlay. Do not remove the webhook row.",
		"# re-enabled for the on-call rota",
		"!!js Number(process.env.DSH_WEBHOOK_PORT ?? 3081)",
		"'127.0.0.1'",
		"id: ui-schedule",
		"id: github-webhook-server",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("install lost %q from the user's patch file:\n%s", want, got)
		}
	}
	if !strings.Contains(got, dshPatchEntryID) {
		t.Fatalf("install did not add Beacon's row:\n%s", got)
	}
}

// Beacon's element goes at the end, where later elements win and where a person adding a row by
// hand would put one.
func TestDshInstallAppendsRatherThanReordering(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(patchPath, []byte("- id: agent-loop\n  config:\n    model: theirs\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	installDshFixture(t, hooksPath)
	got := readFile(t, patchPath)
	if strings.Index(got, "agent-loop") > strings.Index(got, dshPatchEntryID) {
		t.Fatalf("Beacon's element was put ahead of the user's:\n%s", got)
	}
}

// A reinstall repoints the mount instead of adding a second one. The row carries an absolute path,
// so a reinstall after DSH_HOME moved must update it -- skipping would leave the bridge aimed at a
// file that is no longer there.
func TestDshReinstallRepointsTheMountInPlace(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	moved := filepath.Join(t.TempDir(), "moved-home", dshHookFileName)
	if err := addDshPatchEntry(patchPath, moved); err != nil {
		t.Fatalf("addDshPatchEntry: %v", err)
	}

	got := readFile(t, patchPath)
	// Counted on the id line rather than on the bare string: the hooks filename contains the same
	// characters, so a bare count is two for a single correct mount.
	if strings.Count(got, "id: "+dshPatchEntryID) != 1 {
		t.Fatalf("a second mount was added:\n%s", got)
	}
	if !strings.Contains(got, moved) {
		t.Fatalf("the mount was not repointed at %q:\n%s", moved, got)
	}
	if strings.Contains(got, hooksPath) {
		t.Fatalf("the old configPath survived:\n%s", got)
	}
}

// A comment the user wrote above Beacon's row is theirs, and a reinstall must not replace it with
// Beacon's own.
func TestDshReinstallKeepsAUserCommentOnBeaconsRow(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	withComment := strings.Replace(readFile(t, patchPath),
		"- insert:", "# Approved by security 2026-03-02, ticket SEC-1188.\n- insert:", 1)
	if err := os.WriteFile(patchPath, []byte(withComment), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	installDshFixture(t, hooksPath)
	if !strings.Contains(readFile(t, patchPath), "SEC-1188") {
		t.Fatalf("a reinstall discarded the user's comment:\n%s", readFile(t, patchPath))
	}
}

// A patch file Beacon cannot parse is left alone, unlike a hooks file. The two differ because the
// consequences do: this file is where the user's whole plugin tree is tweaked, and dsh's parser is
// not Go's.
func TestDshInstallRefusesAnUnparsablePatchFile(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := "- insert:\n  - id: broken\n   name: bad indent\n"
	if err := os.WriteFile(patchPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := installDshHooks(hooksPath, "/opt/beacon/bin/beacon-hooks", "/log", "/config"); err == nil {
		t.Fatal("install rewrote a patch file it could not parse")
	}
	if got := readFile(t, patchPath); got != theirs {
		t.Fatalf("the patch file was modified anyway:\n%s", got)
	}
}

// A patch file that is a mapping or a scalar is not a patch file. Appending a sequence element to
// it would produce a document that is neither.
func TestDshInstallRefusesAPatchFileThatIsNotAList(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := "plugins:\n  - name: something\n"
	if err := os.WriteFile(patchPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := installDshHooks(hooksPath, "/opt/beacon/bin/beacon-hooks", "/log", "/config"); err == nil {
		t.Fatal("install rewrote a patch file that is not a list of entries")
	}
	if got := readFile(t, patchPath); got != theirs {
		t.Fatalf("the patch file was modified anyway:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Installed-ness
// ---------------------------------------------------------------------------

// Both halves, and the conjunction is the point: a hooks file alone is a file nothing reads, and a
// mount alone is a bridge that registers nothing. Reporting either as installed is how an operator
// ends up believing a machine is monitored when it is not.
func TestDshIsInstalledRequiresBothHalves(t *testing.T) {
	t.Run("both", func(t *testing.T) {
		hooksPath, _ := dshUserPaths(t)
		installDshFixture(t, hooksPath)
		if !isDshInstalledAt(hooksPath) {
			t.Fatal("a complete install reports not installed")
		}
	})
	t.Run("hooks file only", func(t *testing.T) {
		hooksPath, patchPath := dshUserPaths(t)
		installDshFixture(t, hooksPath)
		if err := os.Remove(patchPath); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if isDshInstalledAt(hooksPath) {
			t.Fatal("a hooks file with nothing mounted at it reports installed")
		}
	})
	t.Run("mount only", func(t *testing.T) {
		hooksPath, _ := dshUserPaths(t)
		installDshFixture(t, hooksPath)
		if err := os.Remove(hooksPath); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if isDshInstalledAt(hooksPath) {
			t.Fatal("a mount pointing at a missing hooks file reports installed")
		}
	})
	t.Run("nothing", func(t *testing.T) {
		hooksPath, _ := dshUserPaths(t)
		if isDshInstalledAt(hooksPath) {
			t.Fatal("an untouched machine reports installed")
		}
	})
}

// ---------------------------------------------------------------------------
// Uninstall
// ---------------------------------------------------------------------------

// The property that would otherwise break the runtime. An empty or comments-only patch file does
// not boot dsh -- it is a present-but-unreadable layer, not an ignored one -- so removing Beacon's
// last element must remove the file.
func TestDshUninstallRemovesAPatchFileItWouldOtherwiseEmpty(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	removed, err := removeDshHooks(hooksPath)
	if err != nil {
		t.Fatalf("removeDshHooks: %v", err)
	}
	if !removed {
		t.Fatal("uninstall reported nothing was there")
	}
	if _, err := os.Stat(patchPath); !os.IsNotExist(err) {
		t.Fatalf("the patch file survived as %q; an empty patch layer fails dsh boot",
			readFile(t, patchPath))
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatal("the hooks file survived the uninstall")
	}
}

// A patch file with the user's own rows in it is kept, with only Beacon's element gone.
func TestDshUninstallKeepsTheUsersOwnRows(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := "# keep me\n- insert:\n    - id: theirs\n      name: '@deepseek-ai/dsh-webhook'\n"
	if err := os.WriteFile(patchPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	installDshFixture(t, hooksPath)

	if _, err := removeDshHooks(hooksPath); err != nil {
		t.Fatalf("removeDshHooks: %v", err)
	}
	got := readFile(t, patchPath)
	if !strings.Contains(got, "id: theirs") || !strings.Contains(got, "# keep me") {
		t.Fatalf("uninstall took the user's rows with it:\n%s", got)
	}
	if strings.Contains(got, dshPatchEntryID) {
		t.Fatalf("Beacon's element survived the uninstall:\n%s", got)
	}
}

// An element carrying Beacon's row alongside somebody else's is not Beacon's, and removing it
// would take their rows with it. Beacon only ever writes an element of its own.
func TestDshUninstallDoesNotClaimASharedInsertElement(t *testing.T) {
	_, patchPath := dshUserPaths(t)
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shared := "- insert:\n    - id: " + dshPatchEntryID + "\n      name: '" + dshBridgePackage + "'\n" +
		"    - id: theirs\n      name: '@deepseek-ai/dsh-webhook'\n"
	if err := os.WriteFile(patchPath, []byte(shared), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	removed, err := removeDshPatchEntry(patchPath)
	if err != nil {
		t.Fatalf("removeDshPatchEntry: %v", err)
	}
	if removed {
		t.Fatal("uninstall claimed an element it did not write")
	}
	if got := readFile(t, patchPath); got != shared {
		t.Fatalf("the shared element was modified:\n%s", got)
	}
}

// Uninstalling twice is not an error, and the second time reports that nothing was there.
func TestDshUninstallIsIdempotent(t *testing.T) {
	hooksPath, _ := dshUserPaths(t)
	installDshFixture(t, hooksPath)

	if _, err := removeDshHooks(hooksPath); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	removed, err := removeDshHooks(hooksPath)
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if removed {
		t.Fatal("the second uninstall reported it removed something")
	}
}

// Install, uninstall, install: the machine ends where it started, with one mount and one hooks file.
func TestDshInstallUninstallInstallIsStable(t *testing.T) {
	hooksPath, patchPath := dshUserPaths(t)
	installDshFixture(t, hooksPath)
	first := readFile(t, patchPath)

	if _, err := removeDshHooks(hooksPath); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	installDshFixture(t, hooksPath)

	if got := readFile(t, patchPath); got != first {
		t.Fatalf("the patch file differs after a round trip:\nfirst:\n%s\nsecond:\n%s", first, got)
	}
	if !isDshInstalledAt(hooksPath) {
		t.Fatal("the reinstall did not take")
	}
}
