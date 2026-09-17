package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverOmo reports whether Senpi (the standalone edition of oh-my-openagent) is present and
// whether Beacon is observing it.
//
// Its capability is "plugin" rather than "hooks" or "otel_env" for the same reason Pi's, Oh My
// Pi's and Prime Agent's are: it has neither surface -- no hooks configuration file to merge into
// and no OpenTelemetry export to point at the local collector. Its documented observation surface
// is the TypeScript extension API, so what Beacon installs is one extension file, and what this
// function reports is the state of that file.
func DiscoverOmo() Harness {
	h := Harness{Name: "omo_senpi", DisplayName: "Senpi", Capability: "plugin"}
	// The distributed command is `omo`. It is deliberately probed for over `senpi`: `senpi` is the
	// upstream engine's own binary name, and a machine could have plain, un-OMO'd Senpi installed
	// without oh-my-openagent at all -- reporting that as this harness would claim an install this
	// function did not verify.
	detectExecutable(&h, "omo")

	// A missing home directory leaves the config path empty rather than resolving to a relative
	// path like ".omo/agent/extensions/beacon.ts", which would make discovery report on whatever
	// happens to sit under the current working directory. DiscoverPi, DiscoverOmp and DiscoverPrime
	// guard the same way.
	extensionPath, err := hooks.OmoExtensionPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Senpi extension directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = extensionPath

	// Senpi installed through the omo-ai npm package, into any directory this process did not
	// inherit on PATH, is still detectable by its state directory -- a shell alias, a version
	// manager, or a per-project install all hide the binary while the directory remains. Treating
	// that directory as evidence keeps `endpoint discover` from reporting "not detected" for a
	// runtime the operator is actively using, the same fallback the Pi, Oh My Pi, Prime Agent,
	// opencode, Cursor and Hermes probes make.
	//
	// The directory checked is the one the resolved extension path lives under rather than a
	// rebuilt `~/.omo/agent`, so an OMO_CODING_AGENT_DIR override (or its legacy SENPI_/PI_ fallback)
	// is detected where it actually is instead of being missed at the default location.
	if !h.Detected && dirExists(filepath.Dir(filepath.Dir(extensionPath))) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = omoStatus(extensionPath)
	return h
}

// omoStatus classifies the extension file at path.
//
// The middle case is the one worth spelling out: a beacon.ts that exists without Beacon's marker
// is somebody else's extension sharing the filename, so it is reported as disabled rather than
// enabled. Reporting it as enabled would claim telemetry Beacon is not collecting, and install
// refuses to overwrite the same file for the same reason.
//
// A file carrying Pi's, Oh My Pi's or Prime Agent's marker at this path is also not a Senpi
// install. The four runtimes read different directories today, so it should not happen -- but the
// markers are distinct precisely so that the answer does not depend on that staying true.
func omoStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Beacon Senpi extension was not found"
		}
		return TelemetryMissing, err.Error()
	}
	if !strings.Contains(string(data), hooks.OmoManagedExtensionMarker) {
		return TelemetryDisabled, "Senpi extension file exists but Beacon endpoint extension was not found"
	}
	return TelemetryEnabled, "Beacon Senpi extension is configured"
}
