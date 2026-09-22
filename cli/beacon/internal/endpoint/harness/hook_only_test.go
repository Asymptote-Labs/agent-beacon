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
