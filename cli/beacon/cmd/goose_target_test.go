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

// The install -> status -> uninstall round trip for goose, through the CLI entry points rather
// than the package API.
//
// Wiring a runtime into `beacon endpoint hooks` means six separate places -- install, uninstall,
// status collection, status printing, the repair list and the --all sweep -- plus a registry row,
// and every one of them fails by omission rather than by error. TestEveryHookTargetIsWired proves
// each switch has a case; this proves the cases do the thing their name claims.
func TestEndpointHooksInstallAndUninstallGoose(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GOOSE_PATH_ROOT", "")
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	hooksPath := filepath.Join(home, ".agents", "plugins", "beacon-endpoint", "hooks", "hooks.json")
	if err := installEndpointHookTarget("goose", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(goose) returned error: %v", err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("install did not write %s: %v", hooksPath, err)
	}
	if !strings.Contains(string(data), "--platform goose") {
		t.Fatalf("the hooks file does not carry the goose hook command:\n%s", data)
	}

	// Decoded rather than string-matched, because goose skips a hooks.json it cannot parse with a
	// warn line the operator never sees: a file that is written but malformed looks installed and
	// collects nothing.
	var document struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("the hooks file is not valid JSON: %v", err)
	}
	for _, event := range []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
		"Stop", "SessionEnd",
	} {
		rules := document.Hooks[event]
		if len(rules) != 1 || len(rules[0].Hooks) != 1 {
			t.Errorf("the hooks file does not register exactly one %q action:\n%s", event, data)
			continue
		}
		if rules[0].Hooks[0].Type != "command" {
			t.Errorf("%s action type = %q, want command", event, rules[0].Hooks[0].Type)
		}
	}

	if !endpointhooks.IsGooseInstalled(endpointhooks.GooseOptions{
		Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true,
	}) {
		t.Fatal("IsGooseInstalled = false after installEndpointHookTarget")
	}

	if err := uninstallEndpointHookTarget("goose", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(goose) returned error: %v", err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hooks file behind: %v", err)
	}
	// The plugin directory goes with it: a plugin directory with no hooks file is a plugin goose
	// still discovers and still records in config.yaml's `plugins` map.
	pluginRoot := filepath.Join(home, ".agents", "plugins", "beacon-endpoint")
	if _, err := os.Stat(pluginRoot); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the plugin directory behind: %v", err)
	}
}

// `endpoint hooks repair` walks a hard-coded list rather than the registry, so a runtime absent
// from it is never repaired -- and repair is what fixes a hook whose binary path went stale after
// an upgrade. It fails silently: repair reports success having skipped the runtime entirely.
func TestGooseIsInTheRepairTargetList(t *testing.T) {
	for _, name := range repairTargetOrder() {
		if name == "goose" {
			return
		}
	}
	t.Fatal("goose is missing from the repair target list; `endpoint repair` would silently skip it")
}

// Every spelling a person plausibly types resolves to goose in both namespaces, and the two
// namespaces resolve to different kinds on purpose.
//
// goose is the only runtime with both, under one name: `endpoint install --harness goose`
// configures the OTLP export and `endpoint hooks install --harness goose` installs the hooks. Both
// are wanted on a goose endpoint and neither subsumes the other -- hooks see prompts, tool calls,
// commands and file edits; OTLP carries the token usage, cost, model and reasoning goose puts on no
// hook at all -- so a spelling that resolved in only one namespace would silently offer half the
// integration.
//
// Space-separated spellings are deliberately absent: normalizeHarnessKey folds case and
// underscores but not spaces, so no runtime accepts them and goose is not the place to change that.
func TestGooseHarnessSpellingsResolveInBothNamespaces(t *testing.T) {
	for _, spelling := range []string{
		"goose", "Goose", " goose ", "GOOSE", "codename-goose", "codename_goose",
		"block-goose", "block_goose",
	} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok {
				t.Fatalf("normalizeEndpointTarget(%q) = not found", spelling)
			}
			// The endpoint namespace is the OTLP row: `endpoint install` configures config.yaml.
			if target.Name != "goose" || target.Kind != endpointTargetOTLP {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, want the goose OTLP target", spelling, target)
			}
			// The hook namespace is the hook row, built from a separate alias list so neither row
			// can shadow the other.
			if got, ok := normalizeHookTarget(spelling); !ok || got != "goose" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want goose, true", spelling, got, ok)
			}
		})
	}
}

// GooseAI is a different company's LLM inference service, and accepting either spelling here would
// let someone ask to install hooks for a model provider. The same exclusion NormalizeHarnessName
// makes, made again at the point a person types a name.
func TestGooseAIProviderSpellingsAreNotAGooseTarget(t *testing.T) {
	for _, spelling := range []string{"gooseai", "goose-ai", "goose_ai"} {
		t.Run(spelling, func(t *testing.T) {
			if target, ok := normalizeEndpointTarget(spelling); ok && target.Name == "goose" {
				t.Fatalf("normalizeEndpointTarget(%q) resolved to the goose runtime; that spelling "+
					"names an inference provider", spelling)
			}
		})
	}
}

// The canonical harness name events are written under is itself an accepted spelling, so a row
// read out of the runtime log and passed back to --harness resolves to the runtime it names.
func TestGooseCanonicalHarnessNameIsAnAcceptedTarget(t *testing.T) {
	target, ok := normalizeEndpointTarget("goose")
	if !ok || target.Name != "goose" {
		t.Fatalf("normalizeEndpointTarget(goose) = %+v, %t", target, ok)
	}
	if hookTarget, ok := normalizeHookTarget("goose"); !ok || hookTarget != "goose" {
		t.Fatalf("normalizeHookTarget(goose) = %q, %t", hookTarget, ok)
	}
}

// goose stays in the --all sweep at both levels. Both scopes are real: a user install covers every
// project on the machine, and a project install commits the plugin with the repository. Install and
// uninstall both return on the first target error, so a target wrongly filtered out is not the only
// thing missing -- everything ordered after it in the list is skipped too.
func TestGooseIsSweptAtBothLevels(t *testing.T) {
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })

	for _, level := range []string{"user", "project"} {
		endpointOpts.hookLevel = level
		if !containsTarget(allHookTargetsForLevel(), "goose") {
			t.Fatalf("%s-level --all dropped goose:\n%v", level, allHookTargetsForLevel())
		}
	}
	if userScopeOnlyHookTargets["goose"] {
		t.Fatal("goose is marked user scope only; its project scope is a real install location")
	}
}

// A project-scope install writes into the repository the operator is standing in, and leaves the
// user-scope file alone.
//
// On goose the two are exclusive rather than additive -- it deduplicates plugins by name with
// project scope first -- so the point of this is that one install does not quietly become the
// other, and that an operator who installs at both ends up with one registration rather than two
// copies of every event.
func TestEndpointHooksInstallGooseAtProjectLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GOOSE_PATH_ROOT", "")
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

	if err := installEndpointHookTarget("goose", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(goose, project) returned error: %v", err)
	}
	// os.Getwd resolves symlinks on macOS, where TempDir hands back a /var path that is really
	// /private/var, so the written path is compared through the same resolution rather than against
	// the temp directory name.
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectPath := filepath.Join(resolved, ".agents", "plugins", "beacon-endpoint", "hooks", "hooks.json")
	if _, err := os.ReadFile(projectPath); err != nil {
		t.Fatalf("project install did not write %s: %v", projectPath, err)
	}
	userPath := filepath.Join(home, ".agents", "plugins", "beacon-endpoint", "hooks", "hooks.json")
	if _, err := os.Stat(userPath); !os.IsNotExist(err) {
		t.Fatalf("project install also wrote the user-scope file at %s (err=%v)", userPath, err)
	}
}
