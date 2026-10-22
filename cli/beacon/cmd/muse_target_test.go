package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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

// `endpoint hooks install --all --level project` must install every target whose installer accepts
// project scope, into the repository, and must leave out exactly the ones that refuse it. Both
// directions fail silently: a refusing target left in the sweep aborts it on the first error and
// skips everything ordered after it, and an accepting target wrongly marked user scope only (Kiro,
// OpenHands, whose project file is the documented one) simply never gets installed. An installer
// that ignores --level writes the machine-wide file instead, which is caught by checking HOME.
// Replaces the per-runtime Dsh/Kimi/Kiro/OpenHands sweep and project-level checks.
func TestProjectLevelHookSweepInstallsEveryProjectCapableTargetIntoTheRepository(t *testing.T) {
	prevOpts := endpointOpts
	t.Cleanup(func() { endpointOpts = prevOpts })
	endpointOpts.hookLevel = "user"
	userSweep := allHookTargetsForLevel()
	endpointOpts.hookLevel = "project"
	projectSweep := allHookTargetsForLevel()

	// Each run gets a fresh HOME and repository. DeepSeek Harness is made to look installed, so a
	// dsh install that fell back to user scope would succeed rather than fail for want of a dsh.
	install := func(t *testing.T, args ...string) (homeBefore map[string]string, home, project string, err error) {
		t.Helper()
		home, project = t.TempDir(), t.TempDir()
		testenv.SetHome(t, home)
		for _, key := range []string{"XDG_CONFIG_HOME", "KIRO_HOME", "DSH_HOME", "OH_PERSISTENCE_DIR", "QWEN_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
			t.Setenv(key, "")
		}
		dshtest.Ready(t, filepath.Join(home, ".dsh"), "0.2.1-rc.2")
		homeBefore = runtimeFilesUnder(t, home)
		t.Chdir(project)
		endpointOpts = prevOpts
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs(append([]string{"endpoint", "hooks", "install", "--level", "project",
			"--log-path", filepath.Join(t.TempDir(), "runtime.jsonl")}, args...))
		t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
		return homeBefore, home, project, rootCmd.Execute()
	}

	t.Run("sweep", func(t *testing.T) {
		before, home, project, err := install(t, "--all")
		if err != nil {
			t.Fatalf("project-level --all failed; a target in %v refuses project scope and aborted the sweep: %v", projectSweep, err)
		}
		if after := runtimeFilesUnder(t, home); !reflect.DeepEqual(after, before) {
			t.Fatalf("a project-level install wrote machine-wide runtime config:\n%v", sortedFileNames(after))
		}
		if len(runtimeFilesUnder(t, project)) == 0 {
			t.Fatal("a project-level install wrote nothing into the repository")
		}
	})
	for _, name := range userSweep {
		if containsTarget(projectSweep, name) {
			continue
		}
		t.Run("refuses/"+name, func(t *testing.T) {
			before, home, project, err := install(t, "--harness", name)
			if err == nil {
				t.Fatalf("%s installs at project scope but the project-level --all sweep leaves it out", name)
			}
			if after := runtimeFilesUnder(t, home); !reflect.DeepEqual(after, before) {
				t.Fatalf("the refused %s install wrote machine-wide runtime config anyway:\n%v", name, sortedFileNames(after))
			}
			if written := runtimeFilesUnder(t, project); len(written) != 0 {
				t.Fatalf("the refused %s install wrote into the repository anyway:\n%v", name, sortedFileNames(written))
			}
		})
	}
}

// runtimeFilesUnder maps the files under dir to their content, leaving out Beacon's own .beacon
// tree: the hook binary lives there whatever the level, and no runtime reads it as config.
func runtimeFilesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := filesUnder(t, dir)
	for name := range files {
		if strings.HasPrefix(name, ".beacon"+string(filepath.Separator)) {
			delete(files, name)
		}
	}
	return files
}

func sortedFileNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func containsTarget(targets []string, name string) bool {
	for _, t := range targets {
		if t == name {
			return true
		}
	}
	return false
}
