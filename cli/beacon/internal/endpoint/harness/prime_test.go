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

// Every Prime Agent discovery test empties PATH and the variable that moves its extension
// directory. Without that, the result depends on whether the machine running the suite happens to
// have `prime-agent` installed -- exactly the kind of environment-dependent assertion the repo's
// deterministic-test rule exists to prevent.
func setupPrimeDiscovery(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", "")
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
		t.Fatalf("Capability = %q, want %q -- Prime Agent has no hooks file and no OTel export, so "+
			"the integration is extension-shaped", h.Capability, "plugin")
	}
}

// Prime Agent installed through npm, through its own install.sh into ~/.local/share/prime-agent, or
// into a directory this process did not inherit on PATH is not visible as an executable, so the
// state directory has to count as evidence. The alternative is reporting "not detected" for a
// runtime the operator is actively running, which reads as "Beacon cannot see this" when the truth
// is "Beacon has not been installed into it yet".
func TestDiscoverPrimeTreatsTheStateDirectoryAsEvidence(t *testing.T) {
	home := setupPrimeDiscovery(t)
	if err := os.MkdirAll(filepath.Join(home, ".prime", "agent"), 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverPrime()
	if !h.Detected {
		t.Fatal("DiscoverPrime reported not detected with a Prime Agent agent directory present")
	}
	if h.TelemetryStatus != TelemetryMissing {
		t.Fatalf("TelemetryStatus = %q, want %q -- the runtime is present but Beacon is not "+
			"installed into it", h.TelemetryStatus, TelemetryMissing)
	}
}

// The directory checked is the one the resolved extension path lives under, not a rebuilt
// `~/.prime/agent`, so an overridden install is detected where it actually is rather than being
// missed at the default location.
func TestDiscoverPrimeFollowsTheAgentDirOverride(t *testing.T) {
	home := setupPrimeDiscovery(t)
	agentDir := filepath.Join(home, "elsewhere", "agent")
	t.Setenv("PRIME_AGENT_CODING_AGENT_DIR", agentDir)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	h := DiscoverPrime()
	want := filepath.Join(agentDir, "extensions", "beacon.ts")
	if h.ConfigPath != want {
		t.Fatalf("ConfigPath = %q, want %q", h.ConfigPath, want)
	}
	if !h.Detected {
		t.Fatal("DiscoverPrime reported not detected with an overridden agent directory present")
	}
}

func TestDiscoverPrimeReportsAManagedExtensionAsEnabled(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// "+hooks.PrimeManagedExtensionMarker+"\nexport default function () {}\n")

	h := DiscoverPrime()
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
func TestDiscoverPrimeReportsAnUnmanagedExtensionAsDisabled(t *testing.T) {
	home := setupPrimeDiscovery(t)
	writePrimeExtension(t, home, "// somebody else's extension\nexport default function () {}\n")

	h := DiscoverPrime()
	if h.TelemetryStatus != TelemetryDisabled {
		t.Fatalf("TelemetryStatus = %q, want %q", h.TelemetryStatus, TelemetryDisabled)
	}
}

// A sibling runtime's extension is not a Prime Agent install. The three read different directories
// today, so this should not arise -- but the markers are distinct precisely so the answer does not
// depend on that staying true.
func TestDiscoverPrimeDoesNotAcceptASiblingsMarker(t *testing.T) {
	for name, marker := range map[string]string{
		"pi":  hooks.PiManagedExtensionMarker,
		"omp": hooks.OmpManagedExtensionMarker,
	} {
		t.Run(name, func(t *testing.T) {
			home := setupPrimeDiscovery(t)
			writePrimeExtension(t, home, "// "+marker+"\nexport default function () {}\n")

			if h := DiscoverPrime(); h.TelemetryStatus == TelemetryEnabled {
				t.Fatalf("DiscoverPrime reported %s's extension as a Prime Agent install", name)
			}
		})
	}
}

// The harness name discovery reports must be the canonical one events are written under, or the
// dashboard groups a runtime's discovery row and its telemetry rows separately.
func TestDiscoverPrimeUsesTheCanonicalHarnessName(t *testing.T) {
	setupPrimeDiscovery(t)

	h := DiscoverPrime()
	if got := asymptoteobserve.NormalizeHarnessName(h.Name); got != h.Name {
		t.Fatalf("DiscoverPrime reports %q, which normalizes to %q; discovery and telemetry would "+
			"not join", h.Name, got)
	}
	for _, sibling := range []Harness{DiscoverPi(), DiscoverOmp()} {
		if h.Name == sibling.Name {
			t.Fatalf("Prime Agent and %s discovery report the same harness name", sibling.DisplayName)
		}
	}
}

// A runtime absent from DiscoverAll is a runtime `beacon endpoint discover` never mentions, which
// is how support ships without anyone being able to find it.
func TestDiscoverAllIncludesPrime(t *testing.T) {
	setupPrimeDiscovery(t)

	for _, h := range DiscoverAll() {
		if h.Name == "prime_agent" {
			return
		}
	}
	t.Fatal("DiscoverAll does not include Prime Agent")
}
