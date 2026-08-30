package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverPrime reports whether Prime Agent is present and whether Beacon is observing it.
//
// Prime Agent's capability is "plugin" rather than "hooks" or "otel_env" for the same reason Pi's
// is: it has neither surface. There is no hooks configuration file to merge into, and the trace
// upload its own build offers is a hosted destination rather than an OpenTelemetry exporter Beacon
// could point at the local collector. Its documented observation surface is the TypeScript
// extension API, so what Beacon installs is one extension file, and what this function reports is
// the state of that file.
func DiscoverPrime() Harness {
	h := Harness{Name: "prime_agent", DisplayName: "Prime Agent", Capability: "plugin"}
	detectExecutable(&h, "prime-agent", "prime")

	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".prime", "agent")

	// A missing home directory leaves the config path empty rather than resolving to a relative
	// path like ".prime/agent/extensions/beacon.ts", which would make discovery report on whatever
	// happens to sit under the current working directory.
	extensionPath, err := hooks.PrimeExtensionPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Prime Agent extension directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = extensionPath

	// Prime Agent installed through npm or its own installer may not be on the PATH this process
	// inherited -- a shell alias, a version manager, or a per-project install all hide it -- while
	// its state directory is still there. Treating that directory as evidence keeps
	// `endpoint discover` from reporting "not detected" for a runtime the user is actively using,
	// which is the same fallback the Pi, opencode, Cursor and Hermes probes make for the same
	// reason.
	if !h.Detected && dirExists(agentDir) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = primeStatus(extensionPath)
	return h
}

// primeStatus classifies the extension file at path.
//
// The middle case is the one worth spelling out: a beacon.ts that exists without Beacon's marker is
// somebody else's extension sharing the filename, so it is reported as disabled rather than
// enabled. Reporting it as enabled would claim telemetry Beacon is not collecting, and install
// refuses to overwrite the same file for the same reason.
//
// Pi's marker is not accepted here even though the two files are rendered from one source. A Pi
// extension cannot end up in Prime Agent's directory by any path Beacon takes, so a file carrying
// Pi's marker here was put there by hand, and reporting it as a healthy Prime Agent install would
// claim telemetry that is being written under the wrong harness name.
func primeStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Beacon Prime Agent extension was not found"
		}
		return TelemetryMissing, err.Error()
	}
	if !strings.Contains(string(data), hooks.PrimeManagedExtensionMarker) {
		return TelemetryDisabled, "Prime Agent extension file exists but Beacon endpoint extension was not found"
	}
	return TelemetryEnabled, "Beacon Prime Agent extension is configured"
}
