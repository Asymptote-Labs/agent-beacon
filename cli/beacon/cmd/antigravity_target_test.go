package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/diagnostics"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/harness"
	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// Antigravity never ran the grouped lifecycle schema Beacon used to write. Such a block stays
// installed so repair rewrites it, but `hooks status`, the inventory row and doctor must each say
// it needs a reinstall rather than report it as configured.
func TestLegacyAntigravityHooksAreReportedAsNeedingReinstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	origOpts := endpointOpts
	t.Cleanup(func() { endpointOpts = origOpts })
	endpointOpts.hookLevel = "user"
	endpointOpts.hookHarnesses = "antigravity"
	endpointOpts.allTargets = false
	endpointOpts.jsonOutput = false

	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	writeTestFile(t, path, `{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}]}}`)
	want := endpointhooks.AntigravityLegacyHooksMessage(endpointhooks.LevelUser)

	out, err := captureStdout(t, func() error { return runEndpointHooksStatus(nil, nil) })
	if err != nil {
		t.Fatalf("hooks status: %v", err)
	}
	if !strings.Contains(out, "Antigravity hooks: installed=true needs_reinstall=true path="+path+"\n"+want+"\n") {
		t.Fatalf("hooks status output = %q, want the needs_reinstall line and %q", out, want)
	}

	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
	row := hookStatusesWithConfig([]string{"antigravity"}, cfg)["antigravity"]
	if !row.Installed || row.Status != "needs_reinstall" || row.Message != want {
		t.Fatalf("inventory row = %#v, want installed, needs_reinstall and %q", row, want)
	}

	check := harnessCheck(harness.DiscoverAntigravity(), cfg.LogPath, true)
	if check.Status != diagnostics.StatusWarn || check.Message != want || check.Action != "beacon endpoint hooks install --harness antigravity_cli" {
		t.Fatalf("doctor check = %#v, want a warning carrying %q", check, want)
	}
}

func TestCurrentAntigravityHooksAreConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	origLevel := endpointOpts.hookLevel
	t.Cleanup(func() { endpointOpts.hookLevel = origLevel })
	endpointOpts.hookLevel = "user"

	writeTestFile(t, filepath.Join(home, ".gemini", "config", "hooks.json"), `{"beacon-endpoint":{"Stop":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}}`)
	cfg := endpointconfig.Config{LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}
	if row := hookStatusesWithConfig([]string{"antigravity"}, cfg)["antigravity"]; !row.Installed || row.Status != "configured" {
		t.Fatalf("inventory row = %#v, want configured", row)
	}
}
