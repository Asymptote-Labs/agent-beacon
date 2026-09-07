package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The install -> status -> uninstall round trip for Kiro, through the CLI entry points rather than
// the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims.
func TestEndpointHooksInstallAndUninstallKiro(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIRO_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".kiro", "hooks", "beacon-endpoint.json")
	if err := installEndpointHookTarget("kiro", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(kiro) returned error: %v", err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(data), "--platform kiro") {
		t.Fatalf("the hook file does not carry the Kiro hook command:\n%s", data)
	}
	var document struct {
		Version string `json:"version"`
		Hooks   []struct {
			Trigger string `json:"trigger"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("the hook file is not valid JSON: %v", err)
	}
	if document.Version != "v1" {
		t.Fatalf("version = %q, want v1; Kiro skips a file whose version it does not know", document.Version)
	}
	triggers := map[string]bool{}
	for _, hook := range document.Hooks {
		triggers[hook.Trigger] = true
	}
	for _, trigger := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if !triggers[trigger] {
			t.Errorf("the hook file is missing the %q trigger:\n%s", trigger, data)
		}
	}

	if !endpointhooks.IsKiroInstalled(endpointhooks.KiroOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsKiroInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("kiro", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(kiro) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hook file behind: %v", err)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestKiroIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "kiro" {
			return
		}
	}
	t.Fatal("kiro is missing from the repair target list; `endpoint repair` would silently skip it")
}

// Every spelling a person plausibly types resolves to the one hook target, in both namespaces.
// The IDE and CLI spellings matter most: they are two products to a user and one install to
// Beacon, so both have to land on the same target rather than one of them failing to resolve.
// Space-separated spellings ("kiro ide") are deliberately absent: normalizeHarnessKey folds case
// and underscores but not spaces, so no runtime accepts them and Kiro is not the place to change
// that.
func TestKiroHarnessSpellingsResolveToTheHookTarget(t *testing.T) {
	for _, spelling := range []string{
		"kiro", "Kiro", " kiro ", "KIRO", "kiro-ide", "kiro_ide", "KIRO-IDE",
		"kiro-cli", "kiro_cli", "kiro-code", "kiro_code",
	} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok {
				t.Fatalf("normalizeEndpointTarget(%q) = not found", spelling)
			}
			if target.Name != "kiro" || target.Kind != endpointTargetHook {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, want the kiro hook target", spelling, target)
			}
			if got, ok := normalizeHookTarget(spelling); !ok || got != "kiro" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want kiro, true", spelling, got, ok)
			}
		})
	}
}

// The canonical harness name events are written under is itself an accepted spelling, so a row
// read out of the runtime log and passed back to --harness resolves to the runtime it names.
func TestKiroCanonicalHarnessNameIsAnAcceptedTarget(t *testing.T) {
	target, ok := normalizeEndpointTarget("kiro")
	if !ok || target.Name != "kiro" {
		t.Fatalf("normalizeEndpointTarget(kiro) = %+v, %t", target, ok)
	}
}

// Kiro stays in the --all sweep at both levels. Both scopes are real and neither shadows the other
// -- Kiro merges hook files across scopes -- so filtering either out would drop coverage rather
// than avoid a useless install. Install and uninstall both return on the first target error, so a
// target wrongly filtered out is not the only thing missing: everything ordered after it in the
// list is skipped too.
func TestKiroIsSweptAtBothLevels(t *testing.T) {
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })

	for _, level := range []string{"user", "project"} {
		endpointOpts.hookLevel = level
		if !containsTarget(allHookTargetsForLevel(), "kiro") {
			t.Fatalf("%s-level --all dropped kiro:\n%v", level, allHookTargetsForLevel())
		}
	}
	if userScopeOnlyHookTargets["kiro"] {
		t.Fatal("kiro is marked user scope only; both of its scopes are live and merged")
	}
}

// A project-scope install writes into the repository the operator is standing in, and leaves the
// user-scope file alone. On Kiro the two are additive rather than exclusive, so the point of this
// is that one install does not quietly become the other.
func TestEndpointHooksInstallKiroAtProjectLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIRO_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	project := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "project"

	if err := installEndpointHookTarget("kiro", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(kiro, project) returned error: %v", err)
	}
	// os.Getwd resolves symlinks on macOS, where TempDir hands back a /var path that is really
	// /private/var, so the written path is compared through the same resolution rather than
	// against the temp directory name.
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(resolved, ".kiro", "hooks", "beacon-endpoint.json")); err != nil {
		t.Fatalf("project install did not write .kiro/hooks/beacon-endpoint.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "hooks", "beacon-endpoint.json")); !os.IsNotExist(err) {
		t.Fatalf("project install also wrote the user-scope file: %v", err)
	}
}

// Kiro loads every file in its hooks directory, so an install must be additive: a user's own hook
// file sitting beside Beacon's has to survive both the install and the uninstall. This is the
// property that makes Kiro's installer simpler than OpenHands', and it is worth a test rather than
// a comment because it is the whole reason the installer never reads anything but its own file.
func TestEndpointHooksKiroLeavesOtherHookFilesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KIRO_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	theirs := filepath.Join(home, ".kiro", "hooks", "lint-on-save.json")
	body := `{"version":"v1","hooks":[{"name":"lint-on-save","trigger":"PostFileSave",` +
		`"action":{"type":"command","command":"npx eslint --fix"}}]}`
	if err := os.MkdirAll(filepath.Dir(theirs), 0755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	if err := os.WriteFile(theirs, []byte(body), 0644); err != nil {
		t.Fatalf("write their hook file: %v", err)
	}

	if err := installEndpointHookTarget("kiro", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(kiro) returned error: %v", err)
	}
	if err := uninstallEndpointHookTarget("kiro", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(kiro) returned error: %v", err)
	}

	after, err := os.ReadFile(theirs)
	if err != nil {
		t.Fatalf("their hook file did not survive: %v", err)
	}
	if string(after) != body {
		t.Fatalf("their hook file was modified:\n%s", after)
	}
}
