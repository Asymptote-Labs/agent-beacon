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

// Every alias the hook registry advertises resolves to its own target, in both namespaces, and the
// names of models and vendors that share a runtime's name resolve to nothing. Runtimes that share
// a prefix (pi, omp, prime; opencode, openhands) never resolve to one another: an operator asking
// to instrument one would get another's hooks written where the runtime they meant does not look.
// Canonical log names (muse_code, pi_cli) are accepted so a row read from the runtime log can be
// passed back to --harness.
func TestHookTargetSpellings(t *testing.T) {
	for spelling, want := range map[string]string{
		" KIRO_IDE ": "kiro", "kiro": "kiro", "kiro-cli": "kiro", "kiro-code": "kiro",
		"kimi": "kimi", "kimi-code": "kimi", "kimi-code-cli": "kimi",
		"dsh": "dsh", "deepseek-harness": "dsh", "deepseekharness": "dsh",
		"muse": "muse", "muse_code": "muse",
		"openhands": "openhands", "open-hands": "openhands", "opencode": "opencode",
		"pi": "pi", "pi_cli": "pi",
		"omp": "omp", "oh-my-pi": "omp", "ohmypi": "omp",
		"prime": "prime", "prime-agent": "prime", "primeagent": "prime",
		// Models and vendors, not runtimes.
		"moonshot": "", "kimi-k2": "", "deepseek": "", "deepseek-r1": "",
		"spark": "", "muse-spark": "", "openhands-lm": "",
	} {
		target, ok := normalizeEndpointTarget(spelling)
		hook, hookOK := normalizeHookTarget(spelling)
		if want == "" {
			if ok || hookOK {
				t.Errorf("%q resolved to %+v / %q; it names a model or vendor, not a runtime", spelling, target, hook)
			}
			continue
		}
		if !ok || target.Name != want || target.Kind != endpointTargetHook {
			t.Errorf("normalizeEndpointTarget(%q) = %+v, %t; want the %s hook target", spelling, target, ok, want)
		}
		if !hookOK || hook != want {
			t.Errorf("normalizeHookTarget(%q) = %q, %t; want %s", spelling, hook, hookOK, want)
		}
	}
}

// The project-level --all sweep must not carry a target whose installer refuses project scope.
//
// Install and uninstall both return on the first target error, so leaving such a target in the
// list does not merely fail that one row -- it aborts the sweep and silently skips every target
// ordered after it. Muse is the second target with this property after Hermes, and it sits ahead
// of devin-cli, devin-desktop and antigravity in the list, so a regression here costs those three
// their install with no message naming them.
func TestAllHookTargetsForLevelSkipsUserScopeOnlyTargetsAtProjectLevel(t *testing.T) {
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })

	endpointOpts.hookLevel = "user"
	user := allHookTargetsForLevel()
	for name := range userScopeOnlyHookTargets {
		if !containsTarget(user, name) {
			t.Fatalf("user-level --all dropped %q; it is user scope only, so it belongs there:\n%v", name, user)
		}
	}

	endpointOpts.hookLevel = "project"
	project := allHookTargetsForLevel()
	for name := range userScopeOnlyHookTargets {
		if containsTarget(project, name) {
			t.Fatalf("project-level --all still carries %q, whose installer refuses project scope:\n%v", name, project)
		}
	}

	// The targets ordered after muse and hermes are the ones a first-error abort would swallow.
	for _, name := range []string{"devin-cli", "devin-desktop", "antigravity"} {
		if !containsTarget(project, name) {
			t.Fatalf("project-level --all is missing %q:\n%v", name, project)
		}
	}
}

func containsTarget(targets []string, name string) bool {
	for _, t := range targets {
		if t == name {
			return true
		}
	}
	return false
}
