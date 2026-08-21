package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverPi reports whether Pi (pi.dev) is present and whether Beacon is observing it.
//
// Pi's capability is "plugin" rather than "hooks" or "otel_env" because it has neither surface:
// there is no hooks configuration file to merge into and no OpenTelemetry support to point at the
// local collector. Its documented observation surface is the TypeScript extension API, so what
// Beacon installs is one extension file, and what this function reports is the state of that file.
func DiscoverPi() Harness {
	h := Harness{Name: "pi_cli", DisplayName: "Pi", Capability: "plugin"}
	detectExecutable(&h, "pi")

	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".pi", "agent")

	// A missing home directory leaves the config path empty rather than resolving to a relative
	// path like ".pi/agent/extensions/beacon.ts", which would make discovery report on whatever
	// happens to sit under the current working directory.
	extensionPath, err := hooks.PiExtensionPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Pi extension directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = extensionPath

	// Pi installed through npm/pnpm/bun may not be on the PATH this process inherited -- a shell
	// alias, a version manager, or a per-project install all hide it -- while its state directory
	// is still there. Treating that directory as evidence keeps `endpoint discover` from reporting
	// "not detected" for a runtime the user is actively using, which is the same fallback the
	// opencode, Cursor and Hermes probes make for the same reason.
	if !h.Detected && dirExists(agentDir) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = piStatus(extensionPath)
	return h
}

// piStatus classifies the extension file at path.
//
// The middle case is the one worth spelling out: a beacon.ts that exists without Beacon's marker is
// somebody else's extension sharing the filename, so it is reported as disabled rather than
// enabled. Reporting it as enabled would claim telemetry Beacon is not collecting, and install
// refuses to overwrite the same file for the same reason.
func piStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Beacon Pi extension was not found"
		}
		return TelemetryMissing, err.Error()
	}
	if !strings.Contains(string(data), hooks.PiManagedExtensionMarker) {
		return TelemetryDisabled, "Pi extension file exists but Beacon endpoint extension was not found"
	}
	return TelemetryEnabled, "Beacon Pi extension is configured"
}
