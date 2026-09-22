package harness

import (
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverKiro reports the user-level Kiro hook state. A project hook is not used as the
// machine-wide status because Kiro merges both scopes and the default endpoint install writes the
// user-level file that covers the local IDE and CLI across repositories.
func DiscoverKiro() Harness {
	h := Harness{Name: "kiro", DisplayName: "Kiro", Capability: "hooks"}
	detectExecutable(&h, "kiro-cli")
	status := hooks.KiroHookStatus(hooks.KiroOptions{Level: hooks.LevelUser})
	h.ConfigPath = status.HooksPath
	if !h.Detected && status.HooksPath != "" && dirExists(filepath.Dir(filepath.Dir(status.HooksPath))) {
		h.Detected = true
	}
	if status.Installed {
		h.TelemetryStatus = TelemetryEnabled
		h.Message = status.Message
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = status.Message
	}
	return h
}

// DiscoverMuse reports whether Muse Code is present and whether both halves of Beacon's managed
// hook registration are installed. Muse has no working project-level hook registration.
func DiscoverMuse() Harness {
	h := Harness{Name: "muse_code", DisplayName: "Muse Code", Capability: "hooks"}
	detectExecutable(&h, "muse")
	status := hooks.MuseHookStatus(hooks.MuseOptions{Level: hooks.LevelUser})
	h.ConfigPath = status.SettingsPath
	if !h.Detected && status.SettingsPath != "" && dirExists(filepath.Dir(status.SettingsPath)) {
		h.Detected = true
	}
	if status.Installed {
		h.TelemetryStatus = TelemetryEnabled
		h.Message = status.Message
	} else {
		h.TelemetryStatus = TelemetryMissing
		h.Message = status.Message
	}
	return h
}

// DiscoverOpenHands checks both scopes because OpenHands resolves rather than merges them: the
// repository file used by the CLI and GUI shadows the user-level SDK file.
func DiscoverOpenHands() Harness {
	h := Harness{Name: "openhands", DisplayName: "OpenHands", Capability: "hooks"}
	detectExecutable(&h, "openhands")
	user := hooks.OpenHandsHookStatus(hooks.OpenHandsOptions{Level: hooks.LevelUser})
	project := hooks.OpenHandsHookStatus(hooks.OpenHandsOptions{Level: hooks.LevelProject})
	h.ConfigPath = user.HooksPath
	if project.Installed {
		h.ConfigPath = project.HooksPath
	}
	if !h.Detected {
		h.Detected = (user.HooksPath != "" && dirExists(filepath.Dir(user.HooksPath))) ||
			(project.HooksPath != "" && dirExists(filepath.Dir(project.HooksPath)))
	}
	switch {
	case project.Installed:
		h.TelemetryStatus = TelemetryEnabled
		h.Message = "OpenHands project hooks are installed"
	case user.Installed:
		h.TelemetryStatus = TelemetryEnabled
		h.Message = "OpenHands user hooks are installed for SDK sessions; project hooks are required for the CLI and GUI"
	default:
		h.TelemetryStatus = TelemetryMissing
		h.Message = "OpenHands project hooks are not installed"
	}
	return h
}
