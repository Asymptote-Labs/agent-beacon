package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// Every hook target the CLI advertises must be handled by the install, uninstall, and status
// switches.
//
// The three switches end in `unsupported hook harness %q`, and the target registry is a separate
// list, so adding a runtime to the registry without wiring a switch produces a harness that
// tab-completes, documents, and then refuses to install. Uninstall is the safe probe: it removes
// nothing when nothing is installed, and it fails the same way for an unwired target.
func TestEveryHookTargetIsWired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	seen := map[string]bool{}
	for _, target := range harnessTargets {
		if target.endpointKind != endpointTargetHook && len(target.hookAliases) == 0 {
			continue
		}
		if seen[target.name] {
			continue
		}
		seen[target.name] = true
		t.Run(target.name, func(t *testing.T) {
			if err := uninstallEndpointHookTarget(target.name, cfg); err != nil &&
				strings.Contains(err.Error(), "unsupported hook harness") {
				t.Errorf("%s is a hook target but is not wired into the CLI: %v", target.name, err)
			}
		})
	}
	if !seen["cline"] {
		t.Error("cline is missing from the hook target registry")
	}
}

// The install -> status -> uninstall round trip for Qwen Code, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims.
func TestEndpointHooksInstallAndUninstallQwen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	settings := filepath.Join(home, ".qwen", "settings.json")
	if err := installEndpointHookTarget("qwen", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(qwen) returned error: %v", err)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("install did not write %s: %v", settings, err)
	}
	if !strings.Contains(string(data), "--platform qwen") {
		t.Fatalf("settings do not carry the Qwen hook command:\n%s", data)
	}

	if err := uninstallEndpointHookTarget("qwen", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(qwen) returned error: %v", err)
	}
	data, err = os.ReadFile(settings)
	if err != nil {
		t.Fatalf("uninstall removed the settings file itself: %v", err)
	}
	if strings.Contains(string(data), "--platform qwen") {
		t.Fatalf("uninstall left the Qwen hook behind:\n%s", data)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestQwenIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "qwen" {
			return
		}
	}
	t.Fatal("qwen is missing from the repair target list; `endpoint repair` would silently skip it")
}
