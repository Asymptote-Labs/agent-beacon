package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func setupHookOnlyDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KIRO_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OH_PERSISTENCE_DIR", "")
	return home
}

func TestDiscoverAntigravityDetectsAgyOnPath(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := setupHookOnlyDiscovery(t)
	t.Chdir(home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	agyPath := filepath.Join(binDir, "agy")
	if err := os.WriteFile(agyPath, []byte("#!/bin/sh\necho agy 1.5.1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverAntigravity()
	if !got.Detected || got.ExecutablePath != agyPath || got.Version != "agy 1.5.1" {
		t.Fatalf("DiscoverAntigravity() = %#v, want agy discovered on PATH", got)
	}
	if got.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", got.TelemetryStatus, TelemetryMissing)
	}
}

// Beacon hooks written in the legacy grouped schema are never run by Antigravity, so discovery must
// not report them as enabled, and its message must say why and how to rewrite them.
func TestDiscoverAntigravityReportsLegacyHooks(t *testing.T) {
	home := setupHookOnlyDiscovery(t)
	t.Chdir(t.TempDir())
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"beacon-endpoint":{"Stop":[{"hooks":[{"type":"command","command":"BEACON_ENDPOINT_MODE=1 beacon-hooks --platform antigravity stop"}]}]}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	got := DiscoverAntigravity()
	const want = "Beacon's Antigravity CLI hooks use the legacy schema, which Antigravity does not run; rerun beacon endpoint hooks install --harness antigravity to rewrite them"
	if got.TelemetryStatus != TelemetryDisabled || got.Message != want {
		t.Fatalf("DiscoverAntigravity() = %q (%s), want %q (%s)", got.TelemetryStatus, got.Message, TelemetryDisabled, want)
	}
}

func TestDiscoverKiroDetectsProfileDirectory(t *testing.T) {
	home := setupHookOnlyDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".kiro"), 0755); err != nil {
		t.Fatal(err)
	}
	h := DiscoverKiro()
	if !h.Detected || h.Name != "kiro" || h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("DiscoverKiro() = %#v", h)
	}
}

func TestDiscoverMuseDetectsConfigDirectory(t *testing.T) {
	home := setupHookOnlyDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "muse"), 0755); err != nil {
		t.Fatal(err)
	}
	h := DiscoverMuse()
	if !h.Detected || h.Name != "muse_code" || h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("DiscoverMuse() = %#v", h)
	}
}

func TestDiscoverOpenHandsDetectsPersistenceDirectory(t *testing.T) {
	home := setupHookOnlyDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".openhands"), 0755); err != nil {
		t.Fatal(err)
	}
	h := DiscoverOpenHands()
	if !h.Detected || h.Name != "openhands" || h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("DiscoverOpenHands() = %#v", h)
	}
}
