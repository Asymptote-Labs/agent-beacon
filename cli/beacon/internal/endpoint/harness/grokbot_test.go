package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// The state directory is the one footprint the desktop app is known to leave on every platform,
// so it alone must be enough to detect the app.
func TestDiscoverGrokBotDetectsStateDirectory(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	stateDir := filepath.Join(home, ".grokbot")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "local-exec-daemon.log"), []byte("daemon started\n"), 0o600); err != nil {
		t.Fatalf("write daemon log: %v", err)
	}

	h := DiscoverGrokBot()
	if !h.Detected {
		t.Fatalf("DiscoverGrokBot did not detect ~/.grokbot: %#v", h)
	}
	if h.Name != "grok_bot" || h.DisplayName != "Grok Bot" {
		t.Fatalf("identity = %s/%s, want grok_bot/Grok Bot", h.Name, h.DisplayName)
	}
	if h.ExecutablePath != stateDir || h.ConfigPath != stateDir {
		t.Fatalf("paths = %q/%q, want the state directory %q", h.ExecutablePath, h.ConfigPath, stateDir)
	}
}

// A machine without the app must read as not detected, and the report must still say what the
// harness is and why there is nothing to install, so `beacon endpoint discover` can list the
// runtime as known rather than silently omit it.
func TestDiscoverGrokBotAbsentOnCleanHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))

	h := DiscoverGrokBot()
	if h.Detected {
		t.Fatalf("DiscoverGrokBot detected the app on a clean home: %#v", h)
	}
	if h.Capability != "cloud_export" {
		t.Fatalf("capability = %q, want cloud_export: Grok Bot has no installable collection path", h.Capability)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("telemetry status = %q, want missing", h.TelemetryStatus)
	}
	if h.Message == "" {
		t.Fatal("message is empty; the operator needs to be told the export is the only path")
	}
}

// The state directory on its own carries no telemetry, so detection must never claim any.
func TestDiscoverGrokBotNeverReportsTelemetryEnabled(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".grokbot"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if h := DiscoverGrokBot(); h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("telemetry status = %q, want missing even when the app is present", h.TelemetryStatus)
	}
}

// The application bundle is the second tell on the platforms that have a fixed install location.
func TestDiscoverGrokBotDetectsDesktopBundle(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	var bundle string
	switch runtime.GOOS {
	case "darwin":
		bundle = filepath.Join(home, "Applications", "Grok Bot.app")
	case "windows":
		local := filepath.Join(home, "AppData", "Local")
		t.Setenv("LOCALAPPDATA", local)
		bundle = filepath.Join(local, "Programs", "Grok Bot")
	default:
		t.Skip("no fixed desktop install location on this platform; the state directory is the detection")
	}
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	h := DiscoverGrokBot()
	if !h.Detected || h.ExecutablePath != bundle {
		t.Fatalf("DiscoverGrokBot = %#v, want detection at %q", h, bundle)
	}
}

// Linux has no fixed install location, so the path list is empty there by design rather than
// by omission -- a non-empty list would be a guess the state directory already covers.
func TestGrokBotDesktopPathsPerOS(t *testing.T) {
	if got := grokBotDesktopPathsForOS("linux", "/home/u"); len(got) != 0 {
		t.Fatalf("linux paths = %v, want none", got)
	}
	if got := grokBotDesktopPathsForOS("darwin", "/Users/u"); len(got) != 3 || got[0] != "/Applications/Grok Bot.app" {
		t.Fatalf("darwin paths = %v, want the bundle and support locations", got)
	}
	t.Setenv("LOCALAPPDATA", "")
	if got := grokBotDesktopPathsForOS("windows", `C:\Users\u`); len(got) != 1 || got[0] != filepath.Join(`C:\Users\u`, "AppData", "Local", "Programs", "Grok Bot") {
		t.Fatalf("windows paths = %v, want the per-user Programs location derived from home", got)
	}
}

// A runtime absent from DiscoverAll is one `beacon endpoint discover` never mentions, which is
// where an operator would first learn that Grok Bot is on a machine at all.
func TestDiscoverAllIncludesGrokBot(t *testing.T) {
	for _, h := range DiscoverAll() {
		if h.Name == GrokBotName {
			if h.Capability != GrokBotCapability {
				t.Fatalf("DiscoverAll grok_bot capability = %q, want %q", h.Capability, GrokBotCapability)
			}
			return
		}
	}
	t.Fatalf("DiscoverAll does not include %q", GrokBotName)
}
