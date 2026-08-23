package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
)

// DiscoverQwen reports whether Qwen Code is present and whether Beacon's hooks are installed for it.
//
// Its capability is "hooks" rather than "otel_env" or "otel_config": Qwen Code is a Gemini CLI fork
// but does not carry Gemini's OpenTelemetry export, so there is no endpoint to point at the local
// collector. What it does expose is a documented hook system, and what this function reports is the
// state of Beacon's entries inside Qwen's own settings.json.
func DiscoverQwen() Harness {
	h := Harness{Name: "qwen_code", DisplayName: "Qwen Code", Capability: "hooks"}
	detectExecutable(&h, "qwen")

	home, _ := os.UserHomeDir()

	// A missing home directory leaves the config path empty rather than resolving to the relative
	// ".qwen/settings.json", which is exactly where a *project* install lives. Discovery would
	// otherwise read whatever repository the command ran from and report it as the machine's state.
	// DiscoverCline and DiscoverPi guard the same way.
	settingsPath, err := hooks.QwenSettingsPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Qwen Code settings directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = settingsPath

	// Qwen Code installs through npm, and a version manager, a shell alias or a per-project install
	// all hide the binary from the PATH this process inherited while ~/.qwen is still there.
	// Treating that directory as evidence keeps `endpoint discover` from reporting "not detected"
	// for a runtime the user is actively using -- the same fallback the opencode, Cursor, Pi and
	// Hermes probes make for the same reason.
	if !h.Detected && dirExists(filepath.Join(home, ".qwen")) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = qwenStatus(settingsPath)
	return h
}

// qwenStatus classifies Qwen Code's settings file.
//
// Four outcomes, and the middle two are the ones worth spelling out. A settings file with hooks
// that are not Beacon's is *disabled*, not enabled: reporting it as enabled would claim telemetry
// Beacon is not collecting. Invalid JSON is *misconfigured* rather than missing, because install
// will refuse to touch that file and the user needs to know why -- reporting "not found" for a file
// that is right there sends them looking in the wrong place.
func qwenStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Qwen Code settings file was not found"
		}
		return TelemetryMissing, err.Error()
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return TelemetryMisconfigured, "Qwen Code settings JSON is invalid"
	}
	// Qwen's own kill switch. Beacon's hooks can be present in the file and still never run, and
	// "installed but disabled" is a different problem from "not installed" -- it is fixed in Qwen's
	// settings, not by re-running the installer.
	if disabled, ok := settings["disableAllHooks"].(bool); ok && disabled {
		return TelemetryDisabled, "Qwen Code hooks are disabled by disableAllHooks in settings.json"
	}
	if !strings.Contains(string(data), "--platform qwen") && !strings.Contains(string(data), "--platform=qwen") {
		return TelemetryDisabled, "Qwen Code settings file exists but Beacon endpoint hooks were not found"
	}
	return TelemetryEnabled, "Beacon Qwen Code hooks are configured"
}
