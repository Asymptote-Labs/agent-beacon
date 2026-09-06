package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// museManagedHookFileName is the file Beacon owns outright. It sits beside Muse Code's own
	// settings.json rather than inside a Beacon directory because settings.json points at it by
	// absolute path, and keeping the two together means a person reading their Muse config finds
	// the hooks file next to the key that names it.
	museManagedHookFileName = "beacon-endpoint-hooks.json"

	// museManagedHookMarker stamps the file as Beacon's. Muse tolerates unknown keys at the top
	// level of a managed hooks file -- and only there -- so this is the one place a marker can go.
	museManagedHookMarker = "beacon-managed-muse-hooks:v1"

	// museSettingsFileName is Muse Code's own settings file, which Beacon edits by exactly one key.
	museSettingsFileName = "settings.json"

	// museManagedHooksKey is that key. It takes a path, not a boolean and not a set of hooks:
	// inline `hooks` in settings.json did not fire in testing, and only the managed file did.
	museManagedHooksKey = "managed_hooks_path"
)

// Muse Code hook timeouts are seconds.
//
// Stated at the value rather than left to a reader, for the reason the Qwen constants above spell
// out: the same `timeout` field means milliseconds on Qwen and seconds here, the two settings files
// look alike, and getting it wrong kills every hook mid-write while the install still reports
// success. Seconds here is measured, not assumed -- a hook sleeping 3s under `timeout: 1` was
// killed at about 1s wall.
//
// Events with no entry inherit the host default, which is the right answer for the ones that only
// append a line.
const (
	musePromptSubmitTimeoutSeconds = 30
	museToolTimeoutSeconds         = 10
	museStopTimeoutSeconds         = 45
)

// museAllEventsMatcher matches every event in a group.
//
// The matcher is optional -- a group without one still fires -- but it is an accepted key, and the
// nesting it sits on is not optional at all: handlers listed directly under an event name, or event
// names hoisted out of the `hooks` wrapper, are both ignored in total silence. Writing the matcher
// keeps the shape of what Beacon emits identical to the shape that was verified to fire.
const museAllEventsMatcher = "*"

type MuseOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type MuseStatus struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	HooksPath    string `json:"hooks_path,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

// museHooksFile is the managed hooks file, typed to exactly the keys Muse accepts.
//
// Deliberately its own types rather than the shared settingsHook* ones, and this is a safety
// property rather than tidiness. Muse deserializes matcher groups and handler objects strictly, and
// a rejection is silent: one unrecognized key and that whole event is skipped while the rest of the
// file keeps working, with no warning, no exit code and no log line. `env`, `shell` and a group's
// `enabled` are all confirmed rejections. The shared settingsHookRef carries `shell` and
// `show_output` -- both omitted today, so both harmless today -- but a field added there for
// another runtime would silently remove one event's worth of Muse monitoring and leave the install
// looking healthy. Types that cannot express a key Muse rejects are what stops that.
type museHooksFile struct {
	Description string `json:"description,omitempty"`
	Beacon      string `json:"beacon,omitempty"`
	// SchemaVersion is optional -- a file without it fires -- and is written because it costs
	// nothing and documents which shape this file is.
	SchemaVersion int                        `json:"schema_version,omitempty"`
	Hooks         map[string][]museHookGroup `json:"hooks"`
}

type museHookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []museHookRef `json:"hooks"`
}

// museHookRef holds only accepted handler keys: type, command, timeout, statusMessage. The
// remaining accepted keys -- commandWindows, silent, async -- are not written. commandWindows in
// particular has nothing to carry: Muse Code ships for macOS and Linux only, so there is no Windows
// host to hook.
type museHookRef struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

var museRuntime = hookRuntime{
	displayName: "Muse Code",
	configPath:  museHooksPath,
	install:     installMuseHooks,
	uninstall:   removeMuseHooks,
	isInstalled: isMuseInstalledAt,
}

func InstallMuse(opts MuseOptions) (MuseStatus, error) {
	status, err := installRuntimeHooks(museRuntime, RuntimeOptions(opts))
	if err != nil {
		return MuseStatus{}, err
	}
	return museStatusFromRuntime(status), nil
}

func UninstallMuse(opts MuseOptions) (MuseStatus, error) {
	status, err := uninstallRuntimeHooks(museRuntime, RuntimeOptions(opts))
	if err != nil {
		return MuseStatus{}, err
	}
	return museStatusFromRuntime(status), nil
}

func MuseHookStatus(opts MuseOptions) MuseStatus {
	return museStatusFromRuntime(runtimeHookStatus(museRuntime, RuntimeOptions(opts)))
}

func IsMuseInstalled(opts MuseOptions) bool {
	return isRuntimeInstalled(museRuntime, RuntimeOptions(opts))
}

func museStatusFromRuntime(status runtimeStatus) MuseStatus {
	out := MuseStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
	if status.ConfigPath != "" {
		out.SettingsPath = museSettingsPathForHooks(status.ConfigPath)
	}
	return out
}

// installMuseHooks writes Beacon's managed hooks file and points Muse's settings.json at it.
//
// Two files, in that order, because the second is what makes the first take effect and a
// settings.json naming a file that does not exist yet is a window where Muse silently finds
// nothing.
func installMuseHooks(path, binaryPath, logPath, configPath string) error {
	settingsPath := museSettingsPathForHooks(path)
	// Checked before anything is written. Muse reads exactly one managed hooks file, so pointing
	// the key at Beacon's would disable whatever the existing one registered -- and disable it the
	// way Muse does everything, without a word. Refusing is the only answer that cannot silently
	// take someone's hooks away; overwriting would look like a clean install and quietly end their
	// monitoring.
	if err := museManagedHooksPathIsFree(settingsPath, path); err != nil {
		return err
	}

	prefix := endpointCommandPrefix("muse", binaryPath, logPath, configPath)
	command := func(subcommand string, timeout int) []museHookRef {
		return []museHookRef{{
			Type:          "command",
			Command:       prefix + " " + subcommand,
			Timeout:       timeout,
			StatusMessage: "Beacon endpoint telemetry",
		}}
	}
	group := func(subcommand string, timeout int) []museHookGroup {
		return []museHookGroup{{Matcher: museAllEventsMatcher, Hooks: command(subcommand, timeout)}}
	}

	hooks := museHooksFile{
		Description:   "Beacon managed Muse Code endpoint telemetry hooks.",
		Beacon:        museManagedHookMarker,
		SchemaVersion: 1,
		Hooks: map[string][]museHookGroup{
			"SessionStart":      group("session-start", 0),
			"UserPromptSubmit":  group("prompt-submit", musePromptSubmitTimeoutSeconds),
			"PreToolUse":        group("pre-tool", museToolTimeoutSeconds),
			"PermissionRequest": group("permission-request", museToolTimeoutSeconds),
			"PostToolUse":       group("post-tool", museToolTimeoutSeconds),
			"PreCompact":        group("pre-compact", 0),
			"PostCompact":       group("post-compact", 0),
			"SubagentStart":     group("subagent-start", 0),
			"SubagentStop":      group("subagent-stop", 0),
			"Stop":              group("stop", museStopTimeoutSeconds),
			// PreLLMCall and PostLLMCall are deliberately absent, and their absence is the
			// privacy decision rather than an oversight. Both carry the full `messages` array and
			// the tool schemas -- the entire conversation on every model call, not a preview -- so
			// subscribing would ship far more content to the endpoint log than the prompt and tool
			// arguments Beacon already retains, for signal it mostly already has. PostLLMCall is
			// also expected to fire per response chunk, which makes it the highest-volume event
			// Muse emits by a wide margin. UserPromptSubmit already captures prompt text under the
			// usual retention and redaction controls.
		},
	}
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	return setMuseManagedHooksPath(settingsPath, path)
}

// museManagedHooksPathIsFree reports whether Beacon may claim the managed_hooks_path key.
//
// Free means one of: no settings file, no key, an empty key, or a key that already names Beacon's
// own file. Anything else belongs to somebody and this returns an error naming it, because the
// alternative is taking over a slot whose previous occupant then stops running with no diagnostic
// anywhere.
func museManagedHooksPathIsFree(settingsPath, hooksPath string) error {
	settings, err := readMuseSettings(settingsPath)
	if err != nil {
		return err
	}
	existing := museManagedHooksPathValue(settings)
	if existing == "" || sameFilePath(existing, hooksPath) {
		return nil
	}
	return fmt.Errorf(
		"Muse Code already registers a managed hooks file at %s; Muse reads only one, so Beacon "+
			"will not replace it. Merge Beacon's hooks into that file, or clear %q in %s and run "+
			"the install again",
		existing, museManagedHooksKey, settingsPath)
}

// setMuseManagedHooksPath rewrites settings.json with the key set, preserving every other key.
//
// Decoded into json.RawMessage rather than a typed struct so that settings Beacon has never heard
// of survive the round trip byte for byte. A user's Muse configuration is theirs; Beacon is a guest
// in this file and edits one key of it.
func setMuseManagedHooksPath(settingsPath, hooksPath string) error {
	settings, err := readMuseSettings(settingsPath)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(hooksPath)
	if err != nil {
		return err
	}
	settings[museManagedHooksKey] = encoded
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0600)
}

func readMuseSettings(path string) (map[string]json.RawMessage, error) {
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	// An empty file is not malformed JSON to a person, and treating it as an error would make
	// `install` fail on a settings.json somebody had touched but not written.
	if len(data) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return settings, nil
}

func museManagedHooksPathValue(settings map[string]json.RawMessage) string {
	raw, ok := settings[museManagedHooksKey]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// removeMuseHooks deletes Beacon's hooks file and releases the settings key it claimed.
//
// The key is cleared only when it still names Beacon's file. If something else has claimed it since
// the install, that is now somebody's live registration and deleting it would break their hooks on
// the way out -- an uninstall may remove what it added and nothing more.
func removeMuseHooks(path string) (bool, error) {
	settingsPath := museSettingsPathForHooks(path)
	changed := false

	settings, err := readMuseSettings(settingsPath)
	if err != nil {
		return false, err
	}
	if sameFilePath(museManagedHooksPathValue(settings), path) {
		delete(settings, museManagedHooksKey)
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return false, err
		}
		if err := os.WriteFile(settingsPath, data, 0600); err != nil {
			return false, err
		}
		changed = true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return changed, nil
		}
		return changed, err
	}
	if !isMuseManagedHookFile(data) {
		return changed, nil
	}
	if err := os.Remove(path); err != nil {
		return changed, err
	}
	return true, nil
}

// isMuseInstalledAt requires both halves, because either alone is a broken install that would
// report as working.
//
// A hooks file with no settings key is a file Muse never reads. A settings key pointing at a file
// that is missing or is not Beacon's is a registration with nothing behind it. Muse gives no
// feedback in either case, so a status that checked one half would confidently report telemetry
// that is not being collected.
func isMuseInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil || !isMuseManagedHookFile(data) {
		return false
	}
	settings, err := readMuseSettings(museSettingsPathForHooks(path))
	if err != nil {
		return false
	}
	return sameFilePath(museManagedHooksPathValue(settings), path)
}

func isMuseManagedHookFile(data []byte) bool {
	var hooks museHooksFile
	if err := json.Unmarshal(data, &hooks); err != nil {
		return false
	}
	if hooks.Beacon != museManagedHookMarker {
		return false
	}
	for _, groups := range hooks.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if isEndpointHookCommand(hook.Command, "muse") {
					return true
				}
			}
		}
	}
	return false
}

// sameFilePath compares two configured paths for identity.
//
// Cleaned rather than resolved through the filesystem: this has to answer the same way for a path
// that does not exist yet, which is exactly the case during install and immediately after
// uninstall. filepath.Clean collapses the differences a hand-edited settings file plausibly carries
// -- a trailing separator, a doubled one, an interior "." -- and leaves anything more exotic
// looking like a different path, which is the safe direction: Beacon then declines to touch it.
func sameFilePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func museSettingsPathForHooks(hooksPath string) string {
	return filepath.Join(filepath.Dir(hooksPath), museSettingsFileName)
}

func museHooksPath(level Level) (string, error) {
	dir, err := museConfigDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, museManagedHookFileName), nil
}

// museConfigDir resolves the directory Muse Code keeps settings.json in.
//
// User scope only, and the project case is an error rather than a second path. Muse documents a
// project-level `.muse/hooks.json`, but the shipping build ignores it silently -- so a project
// install would create a file, report success, and collect nothing, which is worse than refusing.
// The error says which scope works instead.
//
// XDG_CONFIG_HOME takes precedence, matching how Muse itself resolves the directory. Worth noting
// for anyone debugging a live install: the variable is stripped from the environment Muse hands to
// hook commands, so a hook cannot resolve this path the same way -- which is why Beacon passes the
// log and config locations to the hook as flags rather than expecting it to find them.
func museConfigDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
			return filepath.Join(base, "muse"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "muse"), nil
	case LevelProject:
		return "", fmt.Errorf(
			"Muse Code has no working project-level hook registration -- its project .muse/hooks.json " +
				"is ignored by the shipping build -- so install Muse hooks at user scope instead")
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
