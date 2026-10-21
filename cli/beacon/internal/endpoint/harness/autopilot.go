package harness

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverAutopilot reports whether Autopilot is present and whether Beacon's hooks are installed
// in its user-level settings.json.
//
// Its capability is "hooks". Autopilot also has an OpenTelemetry export, but it is switched on by
// an environment variable in Autopilot's launch environment rather than by a config file endpoint
// install can edit, so the hooks are what Beacon configures.
func DiscoverAutopilot() Harness {
	h := Harness{Name: "autopilot", DisplayName: "Autopilot", Capability: "hooks"}
	detectExecutable(&h, "autopilot")

	settingsPath, err := hooks.AutopilotSettingsPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Autopilot settings directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = settingsPath

	// Autopilot installs through npm, where a version manager or a per-project install can hide the
	// binary from this process's PATH while its config directory is still there.
	if !h.Detected && dirExists(filepath.Dir(settingsPath)) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = autopilotStatus(settingsPath)
	return h
}

// autopilotStatus classifies Autopilot's settings file. Entries Beacon wrote that the user has
// switched off with `/hooks disable` are reported as disabled rather than enabled: the file says
// installed, and nothing runs.
func autopilotStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Autopilot settings file was not found"
		}
		return TelemetryMissing, err.Error()
	}
	var settings struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return TelemetryMisconfigured, "Autopilot settings JSON is invalid"
	}
	found, enabled := false, false
	for _, raw := range settings.Hooks {
		var entries []struct {
			Command string `json:"command"`
			Enabled *bool  `json:"enabled"`
		}
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			if !hooks.IsEndpointHookCommand(entry.Command, "autopilot") {
				continue
			}
			found = true
			if entry.Enabled == nil || *entry.Enabled {
				enabled = true
			}
		}
	}
	switch {
	case enabled:
		return TelemetryEnabled, "Beacon Autopilot hooks are configured"
	case found:
		return TelemetryDisabled, "Beacon Autopilot hooks are present but disabled (re-enable them with /hooks enable)"
	default:
		return TelemetryDisabled, "Autopilot settings file exists but Beacon endpoint hooks were not found"
	}
}
