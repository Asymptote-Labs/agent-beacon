package harness

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/pelletier/go-toml/v2"
)

// DiscoverKimi reports whether Kimi Code is present and whether Beacon's hooks are installed for
// it.
//
// Its capability is "hooks" rather than "otel_env" or "otel_config", and that is a fact about the
// runtime rather than a gap in this probe: Kimi Code ships no OpenTelemetry export at all. Its
// only telemetry switch is `KIMI_DISABLE_TELEMETRY`, which governs Moonshot's own anonymous
// product reporting and has nothing to do with Beacon -- there is no endpoint to point at the
// local collector, so the hook system is the whole integration surface.
func DiscoverKimi() Harness {
	h := Harness{Name: "kimi_code", DisplayName: "Kimi Code", Capability: "hooks"}
	detectExecutable(&h, "kimi")

	configPath, err := hooks.KimiConfigPath(hooks.LevelUser)
	if err != nil {
		h.TelemetryStatus = TelemetryMissing
		h.Message = "Kimi Code data directory could not be resolved: " + err.Error()
		return h
	}
	h.ConfigPath = configPath

	// Kimi Code installs through npm, and a version manager, a shell alias or a per-project
	// install all hide the binary from the PATH this process inherited while the data root is
	// still there. Treating that directory as evidence keeps `endpoint discover` from reporting
	// "not detected" for a runtime the user is actively using -- the same fallback the opencode,
	// Cursor, Pi, Qwen and Hermes probes make for the same reason.
	//
	// The directory is derived from the config path rather than from $HOME, so a machine that
	// sets KIMI_CODE_HOME is checked where it actually keeps its data.
	if !h.Detected && dirExists(filepath.Dir(configPath)) {
		h.Detected = true
	}

	h.TelemetryStatus, h.Message = kimiStatus(configPath)
	return h
}

// kimiStatus classifies Kimi Code's config file.
//
// Five outcomes, and the two in the middle are the ones worth spelling out.
//
// A config with hooks that are not Beacon's is *disabled*, not enabled: reporting it as enabled
// would claim telemetry Beacon is not collecting.
//
// Invalid TOML is *misconfigured* rather than missing, and on this runtime that is the most
// useful thing discovery can say. Kimi Code refuses to start on a config it cannot load, so the
// user's agent is already broken; Beacon's installer will also refuse to touch the file, and
// reporting "not found" for a file that is right there would send them looking in the wrong
// place.
//
// The fifth is specific to this runtime and is the reason this probe reads the file rather than
// grepping it: a `hooks` key written as an inline array cannot take an appended `[[hooks]]`, so
// the install will refuse. Saying so at discovery time turns a refusal the user meets later into
// something they can fix first.
func kimiStatus(path string) (TelemetryStatus, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TelemetryMissing, "Kimi Code config file was not found"
		}
		return TelemetryMissing, err.Error()
	}
	text := string(data)

	var config map[string]interface{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return TelemetryMisconfigured, "Kimi Code config.toml is not valid TOML; Kimi Code will not start until it is fixed"
	}

	// Matched on the hook command, not on Beacon's comment, for the reason the installer gives:
	// Kimi Code's legacy migration rewrites this file by serializing it, which keeps the entries
	// and drops every comment.
	if strings.Contains(text, "--platform kimi") || strings.Contains(text, "--platform=kimi") {
		return TelemetryEnabled, "Beacon Kimi Code hooks are configured"
	}

	if _, present := config["hooks"]; present && !strings.Contains(text, "[[hooks]]") {
		return TelemetryMisconfigured, "Kimi Code config.toml defines `hooks` as an inline array; " +
			"rewrite it as [[hooks]] blocks before installing Beacon hooks"
	}
	return TelemetryDisabled, "Kimi Code config file exists but Beacon endpoint hooks were not found"
}
