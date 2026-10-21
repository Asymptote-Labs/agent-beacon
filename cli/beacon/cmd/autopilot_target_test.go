package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// The install -> status -> uninstall round trip for Autopilot through the CLI entry points.
// TestEveryHookTargetIsWired proves each switch has a case; this proves the cases write the file
// Autopilot reads and give it back.

func autopilotCLIHome(t *testing.T) (settingsPath string, cfg endpointconfig.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	return filepath.Join(home, ".config", "autopilot", "settings.json"),
		endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
}

func TestEndpointHooksInstallAndUninstallAutopilot(t *testing.T) {
	settingsPath, cfg := autopilotCLIHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"hooks":{"Stop":[{"id":"bell","command":"printf '\\a'"}]},"theme":"dark"}`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := installEndpointHookTarget("autopilot", cfg); err != nil {
		t.Fatalf("installEndpointHookTarget(autopilot): %v", err)
	}
	written, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "--platform autopilot") {
		t.Fatalf("settings do not carry the autopilot hook command:\n%s", written)
	}
	if !endpointhooks.IsAutopilotInstalled(endpointhooks.AutopilotOptions{Level: endpointhooks.LevelUser, LogPath: cfg.LogPath, UserMode: true}) {
		t.Fatal("IsAutopilotInstalled = false after install")
	}

	if err := uninstallEndpointHookTarget("autopilot", cfg); err != nil {
		t.Fatalf("uninstallEndpointHookTarget(autopilot): %v", err)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var want, got interface{}
	_ = json.Unmarshal([]byte(original), &want)
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatalf("uninstall left invalid JSON: %v\n%s", err, after)
	}
	if wantJSON, _ := json.Marshal(want); string(wantJSON) != mustJSON(t, got) {
		t.Fatalf("uninstall did not restore the settings:\nwant %s\ngot  %s", wantJSON, after)
	}
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAutopilotIsInTheRepairAndSweepLists(t *testing.T) {
	inRepair := false
	for _, name := range repairTargetOrder() {
		inRepair = inRepair || name == "autopilot"
	}
	if !inRepair {
		t.Fatal("autopilot is missing from the repair target list; `endpoint repair` would skip it")
	}
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	for _, level := range []string{"user", "project"} {
		endpointOpts.hookLevel = level
		found := false
		for _, name := range allHookTargetsForLevel() {
			found = found || name == "autopilot"
		}
		if !found {
			t.Fatalf("autopilot is missing from the --all list at %s level; Autopilot reads project settings too", level)
		}
	}
}

func TestAutopilotHarnessSpellingsResolveToTheHookTarget(t *testing.T) {
	for _, spelling := range []string{"autopilot", "Autopilot", " AUTOPILOT ", "autopilot-ai", "autopilot_ai"} {
		t.Run(spelling, func(t *testing.T) {
			target, ok := normalizeEndpointTarget(spelling)
			if !ok || target.Name != "autopilot" || target.Kind != endpointTargetHook {
				t.Fatalf("normalizeEndpointTarget(%q) = %+v, %t; want the autopilot hook target", spelling, target, ok)
			}
			if got, ok := normalizeHookTarget(spelling); !ok || got != "autopilot" {
				t.Fatalf("normalizeHookTarget(%q) = %q, %t; want autopilot", spelling, got, ok)
			}
		})
	}
	if auto, ok := autoEndpointTargets["autopilot"]; !ok || len(auto.Hooks) != 1 || auto.Hooks[0] != "autopilot" {
		t.Fatalf("autoEndpointTargets[autopilot] = %+v, %t; want discovery to install the autopilot hooks", auto, ok)
	}
}

func TestAutopilotDiscoveryTracksTheInstall(t *testing.T) {
	settingsPath, cfg := autopilotCLIHome(t)
	row := func(t *testing.T) string {
		t.Helper()
		for _, h := range harness.DiscoverAll() {
			if h.Name == "autopilot" {
				return string(h.TelemetryStatus)
			}
		}
		t.Fatal("autopilot is missing from endpoint discovery")
		return ""
	}
	if got := row(t); got != "missing" {
		t.Fatalf("discovery = %q with no settings file, want missing", got)
	}
	if err := installEndpointHookTarget("autopilot", cfg); err != nil {
		t.Fatal(err)
	}
	if got := row(t); got != "enabled" {
		t.Fatalf("discovery = %q after install, want enabled", got)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("install did not write %s: %v", settingsPath, err)
	}
}
