package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverOmp reports whether Oh My Pi is present and whether Beacon is observing it.
//
// Its capability is "plugin" rather than "hooks" or "otel_env" for the same reason Pi's is: it has
// neither surface -- no hooks configuration file to merge into and no OpenTelemetry export to point
// at the local collector. Its documented observation surface is the TypeScript extension API, so
// what Beacon installs is one extension file, and what this function reports is the state of that
// file.
func DiscoverOmp() Harness {
	h := Harness{Name: "omp", DisplayName: "Oh My Pi", Capability: "plugin"}
	detectExecutable(&h, "omp")

	// A missing home directory leaves the config path empty rather than resolving to a relative
	// path like ".omp/agent/extensions/beacon.ts", which would make discovery report on whatever
	// happens to sit under the current working directory.
	extensionPath, err := hooks.OmpExtensionPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Oh My Pi extension directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = extensionPath

	// Oh My Pi installed through npm/pnpm/bun, or through its own installer into a directory this
	// process did not inherit on PATH, is still detectable by its state directory -- a shell alias,
	// a version manager, or a per-project install all hide the binary while the directory remains.
	// Treating that directory as evidence keeps `endpoint discover` from reporting "not detected"
	// for a runtime the operator is actively using, the same fallback the Pi, opencode, Cursor and
	// Hermes probes make for the same reason.
	//
	// The directory checked is the one the resolved extension path lives under rather than a
	// rebuilt `~/.omp`, so a profile or a PI_CODING_AGENT_DIR override is detected where it
	// actually is instead of being missed at the default location.
	if !h.Detected && dirExists(filepath.Dir(filepath.Dir(extensionPath))) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = ompStatus(extensionPath)
	return h
}

// ompStatus classifies the extension file at path.
//
// The middle case is the one worth spelling out: a beacon.ts that exists without Beacon's marker is
// somebody else's extension sharing the filename, so it is reported as disabled rather than
// enabled. Reporting it as enabled would claim telemetry Beacon is not collecting, and install
// refuses to overwrite the same file for the same reason.
//
// A file carrying Pi's marker at this path is also not an Oh My Pi install. The two runtimes read
// different directories today, so it should not happen -- but the markers are distinct precisely so
// that the answer does not depend on that staying true.
func ompStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Beacon Oh My Pi extension was not found"
		}
		return TelemetryMissing, err.Error()
	}
	if !strings.Contains(string(data), hooks.OmpManagedExtensionMarker) {
		return TelemetryDisabled, "Oh My Pi extension file exists but Beacon endpoint extension was not found"
	}
	return TelemetryEnabled, "Beacon Oh My Pi extension is configured"
}
