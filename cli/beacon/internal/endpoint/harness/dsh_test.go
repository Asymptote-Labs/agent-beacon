package harness

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/dshtest"
)

// Hooks dsh cannot load are files, not telemetry. After a dsh upgrade leaves the installed bridge a
// version behind, discovery must say so rather than report the integration enabled.
func TestDiscoverDshReportsHooksDshCannotLoadAsMisconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dshHome := filepath.Join(home, ".dsh")
	t.Setenv("DSH_HOME", dshHome)
	t.Setenv("PATH", t.TempDir())
	dshtest.InstallCLI(t, "0.2.1-rc.2")
	dshtest.InstallBridge(t, dshHome, "web", "0.2.1-rc.2", "0.2.1-rc.2")
	if _, err := hooks.InstallDsh(hooks.DshOptions{Level: hooks.LevelUser, LogPath: filepath.Join(home, "runtime.jsonl"), UserMode: true}); err != nil {
		t.Fatal(err)
	}
	if h := DiscoverDsh(); h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("discovery with a loadable bridge = %s (%s)", h.TelemetryStatus, h.Message)
	}

	dshtest.InstallCLI(t, "0.2.2")
	h := DiscoverDsh()
	if h.TelemetryStatus != TelemetryMisconfigured || !strings.Contains(h.Message, "dsh is 0.2.2") {
		t.Fatalf("discovery after a dsh upgrade = %s (%s), want misconfigured naming the versions", h.TelemetryStatus, h.Message)
	}
}
