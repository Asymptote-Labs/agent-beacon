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

// The install -> status -> uninstall round trip for OpenHands, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims.
func TestEndpointHooksInstallAndUninstallOpenHands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OH_PERSISTENCE_DIR", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".openhands", "hooks.json")
	if err := installEndpointHookTarget("openhands", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(openhands) returned error: %v", err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(data), "--platform openhands") {
		t.Fatalf("hooks.json does not carry the OpenHands hook command:\n%s", data)
	}
	// The file OpenHands reads is the event map itself, and it forbids fields it does not know --
	// so a stray top-level key is not untidy, it is the whole file failing to load.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v", err)
	}
	for _, event := range []string{"session_start", "user_prompt_submit", "pre_tool_use", "post_tool_use", "stop", "session_end"} {
		if _, ok := document[event]; !ok {
			t.Errorf("hooks.json is missing the %q event:\n%s", event, data)
		}
	}
	if len(document) != 6 {
		t.Fatalf("hooks.json has %d top-level keys, want exactly the six events:\n%s", len(document), data)
	}

	if !endpointhooks.IsOpenHandsInstalled(endpointhooks.OpenHandsOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsOpenHandsInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("openhands", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(openhands) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left an empty hooks.json behind: %v", err)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestOpenHandsIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "openhands" {
			return
		}
	}
	t.Fatal("openhands is missing from the repair target list; `endpoint repair` would silently skip it")
}

// Both spellings a person plausibly types resolve to the hook target, in both namespaces.
func TestOpenHandsHarnessSpellingsResolveToTheHookTarget(t *testing.T) {
	for _, spelling := range []string{"openhands", "OpenHands", " openhands ", "open-hands", "open_hands", "OPEN-HANDS"} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok {
				t.Fatalf("normalizeEndpointTarget(%q) = not found", spelling)
			}
			if target.Name != "openhands" || target.Kind != endpointTargetHook {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, want the openhands hook target", spelling, target)
			}
			if got, ok := normalizeHookTarget(spelling); !ok || got != "openhands" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want openhands, true", spelling, got, ok)
			}
		})
	}
}

// OpenHands LM is All Hands' model family, not the runtime. Accepting it as a harness alias would
// let someone ask Beacon to install hooks for a model and get an install for the agent instead --
// a silent substitution, since the install would then report success under a different name.
func TestOpenHandsModelNamesAreNotHarnessAliases(t *testing.T) {
	for _, spelling := range []string{"openhands-lm", "openhands_lm", "openhands-lm-32b"} {
		t.Run(spelling, func(t *testing.T) {
			if target, ok := normalizeEndpointTarget(spelling); ok {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v; OpenHands LM is a model, not a harness", spelling, target)
			}
		})
	}
}

// opencode and openhands are two separately installed runtimes whose names begin the same way.
// Resolving one to the other would install a plugin for the runtime the operator did not name.
func TestOpenHandsAndOpenCodeAreSeparateTargets(t *testing.T) {
	openhands, ok := normalizeHookTarget("openhands")
	if !ok || openhands != "openhands" {
		t.Fatalf("normalizeHookTarget(openhands) = %q, %t", openhands, ok)
	}
	opencode, ok := normalizeHookTarget("opencode")
	if !ok || opencode != "opencode" {
		t.Fatalf("normalizeHookTarget(opencode) = %q, %t", opencode, ok)
	}
}

// Both scopes are real for OpenHands, unlike Hermes and Muse Code -- project scope is the
// documented location every host reads, so it must stay in the project-level --all sweep. Install
// and uninstall both return on the first target error, so a target wrongly filtered out is not the
// only thing missing: everything ordered after it in the list is skipped too.
func TestOpenHandsIsSweptAtBothLevels(t *testing.T) {
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })

	for _, level := range []string{"user", "project"} {
		endpointOpts.hookLevel = level
		if !containsTarget(allHookTargetsForLevel(), "openhands") {
			t.Fatalf("%s-level --all dropped openhands:\n%v", level, allHookTargetsForLevel())
		}
	}
	if userScopeOnlyHookTargets["openhands"] {
		t.Fatal("openhands is marked user scope only; its project scope is the documented one")
	}
}

// A project-scope install writes into the repository the operator is standing in, which is the
// location the agent server -- and so the CLI and the GUI -- reads.
func TestEndpointHooksInstallOpenHandsAtProjectLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OH_PERSISTENCE_DIR", "")
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

	if err := installEndpointHookTarget("openhands", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(openhands, project) returned error: %v", err)
	}
	// os.Getwd resolves symlinks on macOS, where TempDir hands back a /var path that is really
	// /private/var, so the written path is compared through the same resolution rather than
	// against the temp directory name.
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(resolved, ".openhands", "hooks.json")); err != nil {
		t.Fatalf("project install did not write .openhands/hooks.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".openhands", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("project install also wrote the user-scope file: %v", err)
	}
}
