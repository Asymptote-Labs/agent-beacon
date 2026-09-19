package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The install -> status -> uninstall round trip for DeepSeek Harness, through the CLI entry points
// rather than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims -- and here that means both halves of a two-file
// install, either of which is inert on its own.
func TestEndpointHooksInstallAndUninstallDsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".dsh", "beacon-endpoint-hooks.json")
	patchPath := filepath.Join(home, ".dsh", "cordis.patch.yml")

	if err := installEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(dsh) returned error: %v", err)
	}
	hooks, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(hooks), "--platform dsh") {
		t.Fatalf("the hooks file does not carry the dsh hook command:\n%s", hooks)
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("install did not mount the bridge in %s: %v", patchPath, err)
	}
	if !strings.Contains(string(patch), "@deepseek-ai/dsh-hooks-claude-code") {
		t.Fatalf("the patch file does not mount the hook bridge:\n%s", patch)
	}
	if !strings.Contains(string(patch), hooksPath) {
		t.Fatalf("the mount does not point at the hooks file Beacon wrote:\n%s", patch)
	}

	if !endpointhooks.IsDshInstalled(endpointhooks.DshOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsDshInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(dsh) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hooks file behind: %v", err)
	}
	// The one that would break the runtime rather than merely leave litter: an empty or
	// comments-only patch file fails dsh boot, so an uninstall that emptied it must remove it.
	if _, err := os.Stat(patchPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left a patch file behind; an empty patch layer fails dsh boot: %v", err)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestDshIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "dsh" {
			return
		}
	}
	t.Fatal("dsh is missing from the repair target list; `endpoint repair` would silently skip it")
}

// Every spelling a person plausibly types resolves to the one hook target, in both namespaces.
func TestDshHarnessSpellingsResolveToTheHookTarget(t *testing.T) {
	for _, spelling := range []string{
		"dsh", "DSH", " dsh ", "Dsh",
		"deepseek-harness", "deepseek_harness", "DeepSeek-Harness", "deepseekharness",
	} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok {
				t.Fatalf("normalizeEndpointTarget(%q) = not found", spelling)
			}
			if target.Name != "dsh" || target.Kind != endpointTargetHook {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, want the dsh hook target", spelling, target)
			}
			if got, ok := normalizeHookTarget(spelling); !ok || got != "dsh" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want dsh, true", spelling, got, ok)
			}
		})
	}
}

// Bare "deepseek" names the vendor and the model family. Accepting it as a target would let
// somebody ask to install hooks for a model, which is the same reason NormalizeHarnessName leaves
// it unmapped -- and it is the spelling a user is most likely to reach for, so it is pinned as a
// decision rather than left to be re-litigated.
func TestBareDeepSeekIsNotAHookTarget(t *testing.T) {
	for _, spelling := range []string{"deepseek", "deepseek-chat", "deepseek-r1", "deepseek-v3"} {
		t.Run(spelling, func(t *testing.T) {
			if target, ok := normalizeEndpointTarget(spelling); ok && target.Name == "dsh" {
				t.Fatalf("normalizeEndpointTarget(%q) resolved to the dsh hook target", spelling)
			}
		})
	}
}

// dsh is swept by --all at user scope and filtered out at project scope. The filtering is not
// cosmetic: install and uninstall both return on the first target error, so a target that refuses
// project scope would abort the sweep and silently skip every target ordered after it.
func TestDshIsSweptAtUserScopeOnly(t *testing.T) {
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })

	endpointOpts.hookLevel = "user"
	if !containsTarget(allHookTargetsForLevel(), "dsh") {
		t.Fatalf("user-level --all dropped dsh:\n%v", allHookTargetsForLevel())
	}
	endpointOpts.hookLevel = "project"
	if containsTarget(allHookTargetsForLevel(), "dsh") {
		t.Fatalf("project-level --all kept dsh, whose installer refuses that level:\n%v",
			allHookTargetsForLevel())
	}
	if !userScopeOnlyHookTargets["dsh"] {
		t.Fatal("dsh is not marked user scope only; dsh composes its plugin tree from the " +
			"Harness home, so a repository has nowhere to mount a hook bridge from")
	}
}

// A project-scope install is refused with its reason rather than silently becoming a machine-wide
// one. An operator asking for a repository-scoped install must not get something else.
func TestEndpointHooksDshRefusesProjectLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "project"

	err := installEndpointHookTarget("dsh", cfg)
	if err == nil {
		t.Fatal("a project-level dsh install succeeded; there is no project-scoped hook config")
	}
	if !strings.Contains(err.Error(), "user scope") {
		t.Fatalf("error %q does not tell the operator what to do instead", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".dsh")); !os.IsNotExist(statErr) {
		t.Fatal("the refused install wrote into the Harness home anyway")
	}
}

// The user's own rows in the patch layer survive the round trip. This is the property that makes
// editing a file Beacon does not own defensible, checked here through the CLI rather than the
// package API because the CLI is what an operator actually runs.
func TestEndpointHooksDshLeavesTheUsersPatchRowsAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	patchPath := filepath.Join(home, ".dsh", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(patchPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	theirs := "# our overlay\n- insert:\n    - id: schedule\n      name: '@deepseek-ai/dsh-schedule'\n"
	if err := os.WriteFile(patchPath, []byte(theirs), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := installEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(dsh) returned error: %v", err)
	}
	if err := uninstallEndpointHookTarget("dsh", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(dsh) returned error: %v", err)
	}

	after, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("the user's patch file did not survive: %v", err)
	}
	if !strings.Contains(string(after), "id: schedule") || !strings.Contains(string(after), "# our overlay") {
		t.Fatalf("the user's rows or comments were lost:\n%s", after)
	}
	if strings.Contains(string(after), "dsh-hooks-claude-code") {
		t.Fatalf("Beacon's mount survived the uninstall:\n%s", after)
	}
}
