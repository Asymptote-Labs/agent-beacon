package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const autopilotPlatform = "autopilot"

// Autopilot hook timeouts are milliseconds (`timeoutMs`), and an entry without one gets
// Autopilot's 30000ms default. Named so the unit is stated at the value, the way Qwen's are.
const (
	autopilotPromptSubmitTimeoutMS = 30000
	autopilotToolTimeoutMS         = 10000
	autopilotStopTimeoutMS         = 45000
)

// autopilotEndpointHooks is what Beacon registers in Autopilot's settings.json, one entry per event.
//
// The ids are the ones Autopilot's own Beacon guide (docs/beacon.md in the Autopilot repository)
// tells users to write for Beacon's Claude Code mode, and reusing them is deliberate. Autopilot
// merges its four settings files (legacy and current, global and project) and, within an event, an
// entry with an id already seen replaces the earlier one. So a user who followed that guide before
// this platform existed -- `beacon-hooks --platform claude ...` under these ids, possibly in the
// legacy ~/.config/kimiflare/settings.json that Beacon never edits -- gets the hand-written entries
// superseded by these at runtime rather than every event recorded twice, and gets them back if
// Beacon is uninstalled. Beacon never edits those entries; it recognizes only its own, by command.
//
// PostToolUseFailure is deliberately absent. Autopilot fires PostToolUse for every call, failures
// included, and adds PostToolUseFailure beside it for a failure; binding post-tool to both would
// record every failure twice. No matcher is written: an entry without one matches every tool, which
// is what the policy seam needs on PreToolUse and PermissionRequest.
var autopilotEndpointHooks = []struct {
	event      string
	id         string
	subcommand string
	timeoutMS  int
}{
	{"SessionStart", "beacon-session-start", "session-start", 0},
	{"UserPromptSubmit", "beacon-prompt", "prompt-submit", autopilotPromptSubmitTimeoutMS},
	{"PreToolUse", "beacon-pre-tool", "pre-tool", autopilotToolTimeoutMS},
	{"PostToolUse", "beacon-post-tool", "post-tool", autopilotToolTimeoutMS},
	{"PermissionRequest", "beacon-permission", "permission-request", autopilotToolTimeoutMS},
	{"PreCompact", "beacon-pre-compact", "pre-compact", 0},
	{"Stop", "beacon-stop", "stop", autopilotStopTimeoutMS},
	{"SessionEnd", "beacon-session-end", "session-end", 0},
}

// autopilotHookEntry is the shape of one Autopilot hook. Autopilot's settings are flat -- an event
// maps to a list of entries, each with its own command -- unlike Claude Code's matcher groups, so
// the shared settingsHookGroup writer does not apply.
type autopilotHookEntry struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
}

type AutopilotOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type AutopilotStatus struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

var autopilotRuntime = hookRuntime{
	displayName: "Autopilot",
	configPath:  AutopilotSettingsPath,
	install:     installAutopilotSettings,
	uninstall:   removeAutopilotEndpointHooks,
	isInstalled: isAutopilotInstalledAt,
}

func InstallAutopilot(opts AutopilotOptions) (AutopilotStatus, error) {
	status, err := installRuntimeHooks(autopilotRuntime, RuntimeOptions(opts))
	if err != nil {
		return AutopilotStatus{}, err
	}
	return autopilotStatusFromRuntime(status), nil
}

func UninstallAutopilot(opts AutopilotOptions) (AutopilotStatus, error) {
	status, err := uninstallRuntimeHooks(autopilotRuntime, RuntimeOptions(opts))
	if err != nil {
		return AutopilotStatus{}, err
	}
	return autopilotStatusFromRuntime(status), nil
}

func AutopilotHookStatus(opts AutopilotOptions) AutopilotStatus {
	return autopilotStatusFromRuntime(runtimeHookStatus(autopilotRuntime, RuntimeOptions(opts)))
}

func IsAutopilotInstalled(opts AutopilotOptions) bool {
	return isRuntimeInstalled(autopilotRuntime, RuntimeOptions(opts))
}

func autopilotStatusFromRuntime(status runtimeStatus) AutopilotStatus {
	return AutopilotStatus{
		Installed:    status.Installed,
		BinaryPath:   status.BinaryPath,
		SettingsPath: status.ConfigPath,
		Message:      status.Message,
	}
}

// autopilotSettings is Autopilot's settings.json held as raw JSON: every top-level key and every
// hook entry is written back exactly as it was read, so a user's own hooks keep fields Beacon does
// not model (`enabled`, `failClosed`, `matcher`, `description`, ...).
type autopilotSettings struct {
	values map[string]json.RawMessage
	hooks  map[string]json.RawMessage
}

// readAutopilotSettings refuses the shapes Autopilot's own writer refuses: a file that is not a
// JSON object, and a `hooks` key that is not an object. Autopilot ignores such a file when loading
// hooks, so writing Beacon's entries into a replacement would silently discard what the user had.
func readAutopilotSettings(path string) (autopilotSettings, error) {
	settings := autopilotSettings{values: map[string]json.RawMessage{}, hooks: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return autopilotSettings{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings.values); err != nil || settings.values == nil {
		return autopilotSettings{}, fmt.Errorf("refusing to modify %s: it is not a JSON object; fix or move the file, then try again", path)
	}
	if raw, ok := settings.values["hooks"]; ok {
		if err := json.Unmarshal(raw, &settings.hooks); err != nil || settings.hooks == nil {
			return autopilotSettings{}, fmt.Errorf("refusing to modify %s: its \"hooks\" key is not an object", path)
		}
	}
	return settings, nil
}

func (settings autopilotSettings) entries(path, event string) ([]json.RawMessage, error) {
	raw, ok := settings.hooks[event]
	if !ok {
		return nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("refusing to modify %s: \"hooks.%s\" is not an array", path, event)
	}
	return list, nil
}

// removeBeaconEntries drops Beacon's own entries from every event list, recognized by command so a
// hand-written entry -- including one using Beacon's ids -- is never touched. Values that are not
// arrays belong to events Beacon does not write and are left as they are.
func (settings *autopilotSettings) removeBeaconEntries() bool {
	changed := false
	for event, raw := range settings.hooks {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(list))
		for _, item := range list {
			if isAutopilotBeaconEntry(item) {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == len(list) {
			continue
		}
		if len(kept) == 0 {
			delete(settings.hooks, event)
			continue
		}
		data, _ := json.Marshal(kept)
		settings.hooks[event] = data
	}
	return changed
}

func isAutopilotBeaconEntry(raw json.RawMessage) bool {
	var entry struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return false
	}
	return isSettingsEndpointHookCommand(entry.Command, autopilotPlatform)
}

func (settings autopilotSettings) write(path string) error {
	out := make(map[string]json.RawMessage, len(settings.values)+1)
	for key, value := range settings.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(settings.hooks) > 0 {
		data, err := json.Marshal(settings.hooks)
		if err != nil {
			return err
		}
		out["hooks"] = data
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// installAutopilotSettings merges Beacon's entries into Autopilot's settings.json. Unrelated
// top-level keys and the user's own hooks are preserved, and a reinstall replaces Beacon's entries
// instead of adding a second copy.
func installAutopilotSettings(path, binaryPath, logPath, configPath string) error {
	settings, err := readAutopilotSettings(path)
	if err != nil {
		return err
	}
	for _, hook := range autopilotEndpointHooks {
		if _, err := settings.entries(path, hook.event); err != nil {
			return err
		}
	}
	settings.removeBeaconEntries()
	prefix := endpointCommandPrefix(autopilotPlatform, binaryPath, logPath, configPath)
	for _, hook := range autopilotEndpointHooks {
		list, _ := settings.entries(path, hook.event)
		entry, err := json.Marshal(autopilotHookEntry{ID: hook.id, Command: prefix + " " + hook.subcommand, TimeoutMs: hook.timeoutMS})
		if err != nil {
			return err
		}
		data, err := json.Marshal(append(list, entry))
		if err != nil {
			return err
		}
		settings.hooks[hook.event] = data
	}
	return settings.write(path)
}

func removeAutopilotEndpointHooks(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	settings, err := readAutopilotSettings(path)
	if err != nil {
		return false, err
	}
	if !settings.removeBeaconEntries() {
		return false, nil
	}
	return true, settings.write(path)
}

func isAutopilotInstalledAt(path string) bool {
	settings, err := readAutopilotSettings(path)
	if err != nil {
		return false
	}
	for _, raw := range settings.hooks {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		for _, item := range list {
			if isAutopilotBeaconEntry(item) {
				return true
			}
		}
	}
	return false
}

// AutopilotSettingsPath returns the settings file Beacon merges into for an install level, the
// canonical paths Autopilot reads: `$XDG_CONFIG_HOME/autopilot/settings.json` (default
// `~/.config/autopilot/settings.json`) for the user and `.autopilot/settings.json` in the project.
// Autopilot also still reads the pre-rename `kimiflare` paths; Beacon never writes them.
//
// Exported because harness discovery reports on the same file and must not rebuild the path.
func AutopilotSettingsPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		// Autopilot uses `process.env.XDG_CONFIG_HOME || ~/.config`, so an empty value falls back.
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			if home == "" {
				return "", fmt.Errorf("home directory is empty")
			}
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "autopilot", "settings.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".autopilot", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
