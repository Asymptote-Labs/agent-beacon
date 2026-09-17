package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// openClawHome points discovery at a temporary home with no OpenClaw executable on PATH, which is
// the situation the state-directory fallback exists for.
func openClawHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OPENCLAW_STATE_DIR", "")
	t.Setenv("PATH", t.TempDir())
	return home
}

// A gateway that has run but installed no plugins has `~/.openclaw` and no `~/.openclaw/extensions`.
// That is the common case for a daemonized gateway whose PATH this process did not inherit, and it
// is precisely what the fallback is for -- so detection must not depend on a directory that only
// appears once a plugin is installed.
func TestDiscoverOpenClawDetectsAGatewayWithNoPluginsInstalled(t *testing.T) {
	home := openClawHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".openclaw"), 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverOpenClaw()
	if !h.Detected {
		t.Fatal("a gateway with a state directory and no plugins was reported as not detected")
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("telemetry status = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
}

// Nothing on disk and nothing on PATH is genuinely not detected.
func TestDiscoverOpenClawReportsAbsent(t *testing.T) {
	openClawHome(t)

	h := DiscoverOpenClaw()
	if h.Detected {
		t.Fatal("OpenClaw reported as detected with no state directory and no executable")
	}
}

// OPENCLAW_STATE_DIR moves the state directory, and OpenClaw reads it itself, so discovery has to
// follow it rather than looking under the home directory.
func TestDiscoverOpenClawFollowsTheStateDirOverride(t *testing.T) {
	openClawHome(t)
	state := filepath.Join(t.TempDir(), "gateway-state")
	if err := os.MkdirAll(state, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCLAW_STATE_DIR", state)

	h := DiscoverOpenClaw()
	if !h.Detected {
		t.Fatalf("OPENCLAW_STATE_DIR install was not detected at %s", state)
	}
	want := filepath.Join(state, "extensions", "beacon-endpoint", "beacon.js")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
}

// A plugin directory missing a manifest is one OpenClaw skips during discovery without reporting
// anything, so it must read as disabled rather than enabled.
func TestDiscoverOpenClawReportsAnIncompletePluginAsDisabled(t *testing.T) {
	home := openClawHome(t)
	dir := filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	entry := "// " + hooks.OpenClawManagedPluginMarker + "\nexport default {}\n"
	if err := os.WriteFile(filepath.Join(dir, "beacon.js"), []byte(entry), 0644); err != nil {
		t.Fatal(err)
	}

	h := DiscoverOpenClaw()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("telemetry status = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
}

// An entry without Beacon's marker is somebody else's plugin occupying the id. Reporting it as
// enabled would claim telemetry Beacon is not collecting.
func TestDiscoverOpenClawReportsAnUnmanagedPluginAsDisabled(t *testing.T) {
	home := openClawHome(t)
	dir := filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"beacon.js":            "export default { id: 'beacon-endpoint', register() {} }\n",
		"package.json":         "{}\n",
		"openclaw.plugin.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	h := DiscoverOpenClaw()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("telemetry status = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
}

// A complete, Beacon-written plugin directory is the enabled case.
func TestDiscoverOpenClawReportsACompletePluginAsEnabled(t *testing.T) {
	home := openClawHome(t)
	dir := filepath.Join(home, ".openclaw", "extensions", "beacon-endpoint")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	entry := "// " + hooks.OpenClawManagedPluginMarker + "\nexport default {}\n"
	for name, body := range map[string]string{
		"beacon.js":            entry,
		"package.json":         "{}\n",
		"openclaw.plugin.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	h := DiscoverOpenClaw()
	if !h.Detected {
		t.Fatal("an installed plugin did not mark the gateway as detected")
	}
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("telemetry status = %q, want %q", h.TelemetryStatus, TelemetryEnabled)
	}
	if h.Capability != "plugin" {
		t.Fatalf("capability = %q, want plugin", h.Capability)
	}
}
