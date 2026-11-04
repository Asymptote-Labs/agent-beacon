package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// With XDG_CONFIG_HOME set, OpenCode's config directory is $XDG_CONFIG_HOME/opencode, so that is the
// directory whose presence counts as evidence of the runtime and whose plugin discovery reports.
func TestDiscoverOpenCodeHonorsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	pluginPath := filepath.Join(xdg, "opencode", "plugins", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginPath, []byte("// beacon-managed-opencode-plugin:v1"), 0600); err != nil {
		t.Fatal(err)
	}

	h := DiscoverOpenCode()
	if !h.Detected || h.ConfigPath != pluginPath || h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("DiscoverOpenCode = %#v, want detected, enabled at %q", h, pluginPath)
	}

	// A stale ~/.config/opencode is not evidence once XDG_CONFIG_HOME points elsewhere.
	if err := os.RemoveAll(filepath.Join(xdg, "opencode")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0755); err != nil {
		t.Fatal(err)
	}
	if h := DiscoverOpenCode(); h.Detected {
		t.Fatalf("DiscoverOpenCode detected opencode from ~/.config/opencode with XDG_CONFIG_HOME set elsewhere: %#v", h)
	}
}
