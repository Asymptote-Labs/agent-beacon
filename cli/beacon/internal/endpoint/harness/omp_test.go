package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// writeOmpExtension creates a user-level Oh My Pi extension file with the given contents.
func writeOmpExtension(t *testing.T, home, contents string) string {
	t.Helper()
	path := filepath.Join(home, ".omp", "agent", "extensions", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir omp extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write omp extension: %v", err)
	}
	return path
}

// Every Oh My Pi discovery test empties PATH and the environment variables that move its
// extension directory. Without that, the result depends on whether the machine running the suite
// happens to have `omp` installed or a profile active -- exactly the kind of environment-dependent
// assertion the repo's deterministic-test rule exists to prevent.
func setupOmpDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(name, "")
	}
	return home
}

func TestDiscoverOmpReportsMissingExtension(t *testing.T) {
	home := setupOmpDiscovery(t)

	h := DiscoverOmp()
	if h.Detected {
		t.Fatalf("DiscoverOmp detected Oh My Pi with no executable and no state directory: %#v", h)
	}
	want := filepath.Join(home, ".omp", "agent", "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if h.Capability != "plugin" {
		t.Fatalf("Capability = %q, want %q -- Oh My Pi has no hooks file and no OTel support, so "+
			"the integration is extension-shaped", h.Capability, "plugin")
	}
}

// The directory checked is the one the resolved extension path lives under, not a rebuilt `~/.omp`,
// so a profiled install is detected where it actually is rather than being missed at the default.
func TestDiscoverOmpFollowsTheAgentDirOverride(t *testing.T) {
	home := setupOmpDiscovery(t)
	agentDir := filepath.Join(home, ".omp", "profiles", "work", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverOmp()
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if !h.Detected {
		t.Fatal("DiscoverOmp reported not detected with a profiled Oh My Pi agent directory present")
	}
}

func TestDiscoverOmpReportsAManagedExtensionAsEnabled(t *testing.T) {
	home := setupOmpDiscovery(t)
	writeOmpExtension(t, home, "// "+hooks.OmpManagedExtensionMarker+"\nexport default function () {}\n")

	h := DiscoverOmp()
	if h.TelemetryStatus != TelemetryEnabled {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryEnabled)
	}
	if !h.Detected {
		t.Fatal("an installed Beacon extension implies the runtime is present")
	}
}

// Somebody else's extension can legitimately be called beacon.ts. Reporting it as enabled would
// claim telemetry Beacon is not collecting -- and install refuses to overwrite it for the same
// reason, so "disabled" is the honest reading rather than a pessimistic one.
func TestDiscoverOmpReportsAnUnmanagedExtensionAsDisabled(t *testing.T) {
	home := setupOmpDiscovery(t)
	writeOmpExtension(t, home, "// somebody else's extension\nexport default function () {}\n")

	h := DiscoverOmp()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
}

// Pi's extension is not an Oh My Pi install. The two runtimes read different directories today, so
// this should not arise -- but the markers are distinct precisely so the answer does not depend on
// that staying true.
func TestDiscoverOmpDoesNotAcceptPisMarker(t *testing.T) {
	home := setupOmpDiscovery(t)
	writeOmpExtension(t, home, "// "+hooks.PiManagedExtensionMarker+"\nexport default function () {}\n")

	if h := DiscoverOmp(); h.TelemetryStatus == TelemetryEnabled {
		t.Fatal("DiscoverOmp reported Pi's extension as an Oh My Pi install")
	}
}
