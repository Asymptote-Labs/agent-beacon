package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

// The install -> status -> uninstall round trip for Muse Code, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means five separate switches (install, uninstall,
// status collection, status printing, repair) plus a registry row, and every one of them fails by
// omission rather than by error. TestEveryHookTargetIsWired proves each switch has a case; this
// proves the cases do the thing their name claims -- and for Muse that means both files, since
// either one alone is a broken install the runtime says nothing about.
func TestEndpointHooksInstallAndUninstallMuse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".config", "muse", "beacon-endpoint-hooks.json")
	settingsPath := filepath.Join(home, ".config", "muse", "settings.json")

	if err := installEndpointHookTarget("muse", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(muse) returned error: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(data), "--platform muse") {
		t.Fatalf("hooks file does not carry the Muse hook command:\n%s", data)
	}

	// The half that is easy to leave out and impossible to notice: without this key Muse never
	// opens the file above, and reports nothing about it.
	settings := map[string]interface{}{}
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", settingsPath, err)
	}
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	if got := settings["managed_hooks_path"]; got != hooksPath {
		t.Fatalf("managed_hooks_path = %v, want %q", got, hooksPath)
	}

	if err := uninstallEndpointHookTarget("muse", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(muse) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hooks file behind: %v", err)
	}
	settingsData, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("uninstall removed settings.json itself: %v", err)
	}
	if strings.Contains(string(settingsData), "managed_hooks_path") {
		t.Fatalf("uninstall left a registration pointing at a deleted file:\n%s", settingsData)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestMuseIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "muse" {
			return
		}
	}
	t.Fatal("muse is missing from the repair target list; `endpoint repair` would silently skip it")
}

// Both spellings a person plausibly types resolve to the hook target, in both namespaces.
func TestMuseHarnessSpellingsResolveToTheHookTarget(t *testing.T) {
	for _, spelling := range []string{"muse", "Muse", " muse ", "muse-code", "muse_code", "MUSE-CODE"} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok {
				t.Fatalf("normalizeEndpointTarget(%q) = not found", spelling)
			}
			if target.Name != "muse" || target.Kind != endpointTargetHook {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, want the muse hook target", spelling, target)
			}
			if got, ok := normalizeHookTarget(spelling); !ok || got != "muse" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want muse, true", spelling, got, ok)
			}
		})
	}
}

// Muse Spark is the model, not the runtime. Accepting it as a harness alias would let someone ask
// Beacon to install hooks for a model and get an install for the agent instead -- a silent
// substitution, since the install would then report success under a different name.
func TestMuseSparkIsNotAHarnessAlias(t *testing.T) {
	for _, spelling := range []string{"spark", "muse-spark", "muse_spark"} {
		t.Run(spelling, func(t *testing.T) {
			if target, ok := normalizeEndpointTarget(spelling); ok {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v; Muse Spark is a model, not a harness", spelling, target)
			}
		})
	}
}
