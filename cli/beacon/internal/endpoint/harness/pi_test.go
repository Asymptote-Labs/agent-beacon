package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// writePiExtension creates a user-level Pi extension file with the given contents.
func writePiExtension(t *testing.T, home, contents string) string {
	t.Helper()
	path := filepath.Join(home, ".pi", "agent", "extensions", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir pi extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write pi extension: %v", err)
	}
	return path
}

// Every Pi discovery test empties PATH. Without that, the result depends on whether the machine
// running the suite happens to have a `pi` command installed, which is exactly the kind of
// environment-dependent assertion the repo's deterministic-test rule exists to prevent.
func setupPiDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	return home
}

func TestDiscoverPiReportsMissingExtension(t *testing.T) {
	home := setupPiDiscovery(t)

	h := DiscoverPi()
	if h.Detected {
		t.Fatalf("DiscoverPi detected Pi with no executable and no state directory: %#v", h)
	}
	want := filepath.Join(home, ".pi", "agent", "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if h.Capability != "plugin" {
		t.Fatalf("Capability = %q, want %q -- Pi has no hooks file and no OTel support, so the "+
			"integration is extension-shaped", h.Capability, "plugin")
	}
}

// Pi installed through a version manager or a per-project npm install is not on the PATH this
// process inherited, so the state directory has to count as evidence. The alternative is reporting
// "not detected" for a runtime the user is actively running, which reads as "Beacon cannot see
// this" when the truth is "Beacon has not been installed into it yet".
func TestDiscoverPiDetectsStateDirectoryWithoutExecutable(t *testing.T) {
	home := setupPiDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0755); err != nil {
		t.Fatalf("mkdir pi agent dir: %v", err)
	}

	h := DiscoverPi()
	if !h.Detected {
		t.Fatalf("DiscoverPi did not detect Pi from its state directory: %#v", h)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q; a detected runtime with no extension is not "+
			"instrumented", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverPiReportsEnabledForManagedExtension(t *testing.T) {
	home := setupPiDiscovery(t)
	writePiExtension(t, home, "// "+hooks.PiManagedExtensionMarker+"\nexport default () => {}\n")

	h := DiscoverPi()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("TelemetryStatus = %q, want %q; message=%q", h.TelemetryStatus, TelemetryEnabled, h.Message)
	}
	if !h.Detected {
		t.Fatalf("DiscoverPi did not detect Pi despite a managed extension being installed: %#v", h)
	}
}

// A beacon.ts that Beacon did not write is somebody else's extension sharing the filename.
// Reporting it as enabled would claim telemetry that is not being collected -- the failure mode
// where a dashboard shows a runtime as covered and no events ever arrive.
func TestDiscoverPiReportsDisabledForUnmanagedExtension(t *testing.T) {
	home := setupPiDiscovery(t)
	writePiExtension(t, home, "export default function (pi) { /* not Beacon's */ }\n")

	h := DiscoverPi()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q; message=%q", h.TelemetryStatus, TelemetryDisabled, h.Message)
	}
}

// The marker is a version contract with the extension source. A file carrying an older marker must
// not read as enabled, or a repair would leave a stale extension in place believing it current.
func TestDiscoverPiReportsDisabledForStaleMarkerVersion(t *testing.T) {
	home := setupPiDiscovery(t)
	writePiExtension(t, home, "// beacon-managed-pi-extension:v0\nexport default () => {}\n")

	h := DiscoverPi()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q for a superseded marker version; message=%q",
			h.TelemetryStatus, TelemetryDisabled, h.Message)
	}
}

// PI_CODING_AGENT_DIR moves Pi's agent directory, so discovery has to report the extension, and
// look for the state directory, where the override puts them rather than under ~/.pi/agent.
func TestDiscoverPiFollowsTheAgentDirOverride(t *testing.T) {
	home := setupPiDiscovery(t)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverPi()
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if !h.Detected {
		t.Fatal("DiscoverPi reported not detected with an overridden agent directory present")
	}
}
