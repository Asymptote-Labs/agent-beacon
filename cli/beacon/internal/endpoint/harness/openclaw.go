package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverOpenClaw reports whether OpenClaw Gateway is present and whether Beacon is observing it.
//
// Its capability is "plugin": the plugin path is what collects the agent's work, and it is the one
// `beacon endpoint hooks install --harness openclaw` writes. OpenClaw also has an OTLP surface --
// its own `diagnostics-otel` plugin, configured through `beacon endpoint integrations openclaw` --
// but that one is set up inside OpenClaw rather than by Beacon, carries gateway traces and metrics
// rather than agent actions, and by OpenClaw's default carries no content at all. Reporting the
// capability that Beacon installs is what makes `endpoint discover` answer the question an
// operator is actually asking.
func DiscoverOpenClaw() Harness {
	h := Harness{Name: "openclaw", DisplayName: "OpenClaw Gateway", Capability: "plugin"}
	detectExecutable(&h, "openclaw")

	// A missing home directory leaves the config path empty rather than resolving to a relative
	// path like ".openclaw/extensions/beacon-endpoint/beacon.js", which would make discovery
	// report on whatever happens to sit under the current working directory.
	entryPath, err := hooks.OpenClawEntryPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "OpenClaw plugin directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = entryPath

	// OpenClaw installed through npm, Docker, or a service manager that this process did not
	// inherit on PATH is still detectable by its state directory -- and a gateway is more likely
	// than most runtimes to be running as a daemon under another account's PATH. Treating the
	// directory as evidence keeps `endpoint discover` from reporting "not detected" for a gateway
	// the operator is actively running, the same fallback the Pi, Oh My Pi, opencode, Cursor and
	// Hermes probes make for the same reason.
	//
	// The directory checked is the extensions root the resolved entry lives under, two levels up
	// from the entry file, so an OPENCLAW_STATE_DIR override is detected where it actually is
	// rather than being missed at the default location.
	if !h.Detected && dirExists(filepath.Dir(filepath.Dir(entryPath))) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = openClawStatus(entryPath)
	return h
}

// openClawStatus classifies the plugin directory holding entryPath.
//
// The middle case is the one worth spelling out: an entry that exists without Beacon's marker is
// somebody else's plugin occupying the `beacon-endpoint` id, so it is reported as disabled rather
// than enabled. Reporting it as enabled would claim telemetry Beacon is not collecting, and
// install refuses to overwrite the same file for the same reason.
//
// A directory missing a manifest is reported as disabled too, and this is the failure this probe
// exists to catch. OpenClaw skips such a directory during discovery and says nothing about it, so
// a plugin that never loaded is indistinguishable from one that loaded and saw no activity --
// unless something looks at the directory.
func openClawStatus(entryPath string) (TelemetryStatus, string) {
	data, err := os.ReadFile(entryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Beacon OpenClaw plugin was not found"
		}
		return TelemetryMissing, err.Error()
	}
	if !strings.Contains(string(data), hooks.OpenClawManagedPluginMarker) {
		return TelemetryDisabled, "OpenClaw plugin entry exists but Beacon endpoint plugin was not found"
	}
	dir := filepath.Dir(entryPath)
	for _, name := range []string{"package.json", "openclaw.plugin.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return TelemetryDisabled, "Beacon OpenClaw plugin is missing " + name + ", so OpenClaw will not load it"
		}
	}
	return TelemetryEnabled, "Beacon OpenClaw plugin is configured"
}
