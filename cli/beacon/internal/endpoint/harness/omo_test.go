package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

// writeOmoExtension creates a user-level Senpi extension file with the given contents.
func writeOmoExtension(t *testing.T, home, contents string) string {
	t.Helper()
	path := filepath.Join(home, ".omo", "agent", "extensions", "beacon.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir omo extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write omo extension: %v", err)
	}
	return path
}

// Every Senpi discovery test empties PATH and the variables that move its extension directory.
// Without that, the result depends on whether the machine running the suite happens to have `omo`
// installed -- exactly the kind of environment-dependent assertion the repo's deterministic-test
// rule exists to prevent.
func setupOmoDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OMO_CODING_AGENT_DIR", "")
	t.Setenv("SENPI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	return home
}

func TestDiscoverOmoReportsMissingExtension(t *testing.T) {
	home := setupOmoDiscovery(t)

	h := DiscoverOmo()
	if h.Detected {
		t.Fatalf("DiscoverOmo detected Senpi with no executable and no state directory: %#v", h)
	}
	want := filepath.Join(home, ".omo", "agent", "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryMissing)
	}
	if h.Capability != "plugin" {
		t.Fatalf("Capability = %q, want %q -- Senpi has no hooks file and no OTel export, so the "+
			"integration is extension-shaped", h.Capability, "plugin")
	}
}

// The directory checked is the one the resolved extension path lives under, not a rebuilt
// `~/.omo/agent`, so an overridden install is detected where it actually is rather than being
// missed at the default location.
func TestDiscoverOmoFollowsTheAgentDirOverride(t *testing.T) {
	home := setupOmoDiscovery(t)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv("OMO_CODING_AGENT_DIR", agentDir)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverOmo()
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if !h.Detected {
		t.Fatal("DiscoverOmo reported not detected with an overridden agent directory present")
	}
}

func TestDiscoverOmoReportsAManagedExtensionAsEnabled(t *testing.T) {
	home := setupOmoDiscovery(t)
	writeOmoExtension(t, home, "// "+hooks.OmoManagedExtensionMarker+"\nexport default function () {}\n")

	h := DiscoverOmo()
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
func TestDiscoverOmoReportsAnUnmanagedExtensionAsDisabled(t *testing.T) {
	home := setupOmoDiscovery(t)
	writeOmoExtension(t, home, "// somebody else's extension\nexport default function () {}\n")

	h := DiscoverOmo()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
}

// A sibling runtime's extension is not a Senpi install. The four read different directories today,
// so this should not arise -- but the markers are distinct precisely so the answer does not depend
// on that staying true.
func TestDiscoverOmoDoesNotAcceptASiblingsMarker(t *testing.T) {
	for name, marker := range map[string]string{
		"pi":    hooks.PiManagedExtensionMarker,
		"omp":   hooks.OmpManagedExtensionMarker,
		"prime": hooks.PrimeManagedExtensionMarker,
	} {
		t.Run(name, func(t *testing.T) {
			home := setupOmoDiscovery(t)
			writeOmoExtension(t, home, "// "+marker+"\nexport default function () {}\n")

			if h := DiscoverOmo(); h.TelemetryStatus == TelemetryEnabled {
				t.Fatalf("DiscoverOmo reported %s's extension as a Senpi install", name)
			}
		})
	}
}
