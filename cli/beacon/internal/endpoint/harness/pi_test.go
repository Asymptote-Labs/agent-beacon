package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
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

func TestDiscoverPiDetectsExecutableOnPath(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	piPath := filepath.Join(binDir, "pi")
	if err := os.WriteFile(piPath, []byte("#!/bin/sh\necho pi 0.4.1\n"), 0755); err != nil {
		t.Fatalf("write fake pi executable: %v", err)
	}

	h := DiscoverPi()
	if !h.Detected {
		t.Fatalf("DiscoverPi did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != piPath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, piPath)
	}
	if h.Version != "pi 0.4.1" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
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

// Discovery has to be registered to be reachable: DiscoverPi existing but missing from DiscoverAll
// is invisible in `endpoint discover`, `endpoint status`, and the agent.detected events they write.
func TestDiscoverAllIncludesPi(t *testing.T) {
	setupPiDiscovery(t)

	for _, h := range DiscoverAll() {
		if h.Name == "pi_cli" {
			return
		}
	}
	t.Fatal("DiscoverAll() does not include pi_cli")
}

// The canonical harness name is what events are grouped by, so discovery must report the same
// spelling the normalizer produces. Reporting "pi" here while the hook path writes "pi_cli" would
// split one runtime across two names in every query and dashboard that groups on it -- the same
// defect that once recorded a single Claude Code session as both "claude" and "claude_code".
//
// Asserted against the normalizer rather than against a literal so the two cannot drift apart in
// one direction only.
func TestDiscoverPiUsesCanonicalHarnessName(t *testing.T) {
	setupPiDiscovery(t)

	name := DiscoverPi().Name
	if name != "pi_cli" {
		t.Fatalf("DiscoverPi().Name = %q, want %q", name, "pi_cli")
	}
	if got := asymptoteobserve.NormalizeHarnessName(name); got != name {
		t.Fatalf("NormalizeHarnessName(%q) = %q; discovery must report the canonical name so the "+
			"hook and OTLP paths agree", name, got)
	}
}
