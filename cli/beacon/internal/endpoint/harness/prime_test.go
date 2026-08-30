package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// writePrimeExtension creates a user-level Prime Agent extension file with the given contents.
func writePrimeExtension(t *testing.T, home, contents string) string {
	t.Helper()
	path := filepath.Join(home, ".prime", "agent", "extensions", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir prime extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write prime extension: %v", err)
	}
	return path
}

// Every Prime Agent discovery test empties PATH. Without that, the result depends on whether the
// machine running the suite happens to have a `prime-agent` command installed, which is exactly the
// kind of environment-dependent assertion the repo's deterministic-test rule exists to prevent.
func setupPrimeDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	return home
}

func TestDiscoverPrimeReportsMissingExtension(t *testing.T) {
	home := setupPrimeDiscovery(t)

	h := DiscoverPrime()
	if h.Detected {
		t.Fatalf("DiscoverPrime detected Prime Agent with no executable and no state directory: %#v", h)
	}
	want := filepath.Join(home, ".prime", "agent", "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if h.Capability != "plugin" {
		t.Fatalf("Capability = %q, want %q -- Prime Agent has no hooks file and no OTel exporter "+
			"Beacon can point at the local collector, so the integration is extension-shaped",
			h.Capability, "plugin")
	}
}

// Prime Agent installed through a version manager or its own installer is not on the PATH this
// process inherited, so the state directory has to count as evidence. The alternative is reporting
// "not detected" for a runtime the user is actively running.
func TestDiscoverPrimeDetectsStateDirectoryWithoutExecutable(t *testing.T) {
	home := setupPrimeDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".prime", "agent"), 0755); err != nil {
		t.Fatalf("mkdir prime agent dir: %v", err)
	}

	h := DiscoverPrime()
	if !h.Detected {
		t.Fatalf("DiscoverPrime did not detect Prime Agent from its state directory: %#v", h)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q; a detected runtime with no extension is not "+
			"instrumented", h.TelemetryStatus, TelemetryMissing)
	}
}

func TestDiscoverPrimeDetectsExecutableOnPath(t *testing.T) {
	testenv.RequirePOSIXExecutableFixtures(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	primePath := filepath.Join(binDir, "prime-agent")
	if err := os.WriteFile(primePath, []byte("#!/bin/sh\necho prime-agent 0.8.1\n"), 0755); err != nil {
		t.Fatalf("write fake prime-agent executable: %v", err)
	}

	h := DiscoverPrime()
	if !h.Detected {
		t.Fatalf("DiscoverPrime did not detect executable on PATH: %#v", h)
	}
	if h.ExecutablePath != primePath {
		t.Fatalf("ExecutablePath = %q, want %q", h.ExecutablePath, primePath)
	}
	if h.Version != "prime-agent 0.8.1" {
		t.Fatalf("Version = %q, want fake executable version", h.Version)
	}
}

func TestDiscoverPrimeReportsEnabledForManagedExtension(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// "+hooks.PrimeManagedExtensionMarker+"\nexport default () => {}\n")

	h := DiscoverPrime()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("TelemetryStatus = %q, want %q; message=%q", h.TelemetryStatus, TelemetryEnabled, h.Message)
	}
	if !h.Detected {
		t.Fatalf("DiscoverPrime did not detect Prime Agent despite a managed extension being installed: %#v", h)
	}
}

// A beacon.ts that Beacon did not write is somebody else's extension sharing the filename.
// Reporting it as enabled would claim telemetry that is not being collected.
func TestDiscoverPrimeReportsDisabledForUnmanagedExtension(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "export default function (pi) { /* not Beacon's */ }\n")

	h := DiscoverPrime()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q; message=%q", h.TelemetryStatus, TelemetryDisabled, h.Message)
	}
}

// Both runtimes' extensions are rendered from one source, which makes this the mistake worth
// guarding: a file carrying Pi's marker in Prime Agent's directory forwards its events as Pi's, so
// reporting it as a healthy Prime Agent install would claim telemetry that is arriving under the
// wrong harness name.
func TestDiscoverPrimeRejectsAPiExtension(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// "+hooks.PiManagedExtensionMarker+"\nexport default () => {}\n")

	h := DiscoverPrime()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q for a Pi extension in Prime Agent's directory; message=%q",
			h.TelemetryStatus, TelemetryDisabled, h.Message)
	}
}

// The marker is a version contract with the extension source. A file carrying an older marker must
// not read as enabled, or a repair would leave a stale extension in place believing it current.
func TestDiscoverPrimeReportsDisabledForStaleMarkerVersion(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// beacon-managed-prime-extension:v0\nexport default () => {}\n")

	h := DiscoverPrime()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q for a superseded marker version; message=%q",
			h.TelemetryStatus, TelemetryDisabled, h.Message)
	}
}

// Discovery has to be registered to be reachable: DiscoverPrime existing but missing from
// DiscoverAll is invisible in `endpoint discover`, `endpoint status`, and the agent.detected events
// they write.
func TestDiscoverAllIncludesPrime(t *testing.T) {
	setupPrimeDiscovery(t)

	for _, h := range DiscoverAll() {
		if h.Name == "prime_agent" {
			return
		}
	}
	t.Fatal("DiscoverAll() does not include prime_agent")
}

// The canonical harness name is what events are grouped by, so discovery must report the same
// spelling the normalizer produces. Asserted against the normalizer rather than against a literal
// so the two cannot drift apart in one direction only.
func TestDiscoverPrimeUsesCanonicalHarnessName(t *testing.T) {
	setupPrimeDiscovery(t)

	name := DiscoverPrime().Name
	if name != "prime_agent" {
		t.Fatalf("DiscoverPrime().Name = %q, want %q", name, "prime_agent")
	}
	if got := asymptoteobserve.NormalizeHarnessName(name); got != name {
		t.Fatalf("NormalizeHarnessName(%q) = %q; discovery must report the canonical name so the "+
			"hook and OTLP paths agree", name, got)
	}
}

// Two runtimes, two directories. A Prime Agent install must not make Pi look instrumented, or an
// operator reads one install as covering both products.
func TestPrimeAndPiDiscoveryDoNotSeeEachOther(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// "+hooks.PrimeManagedExtensionMarker+"\nexport default () => {}\n")

	if pi := DiscoverPi(); pi.TelemetryStatus == TelemetryEnabled {
		t.Fatalf("a Prime Agent install reported Pi as instrumented: %#v", pi)
	}
}
