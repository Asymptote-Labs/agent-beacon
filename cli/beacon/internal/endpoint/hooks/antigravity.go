package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/hookcmd"
)

const antigravityBeaconHookName = "beacon-endpoint"

type AntigravityOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type AntigravityStatus struct {
	Installed bool `json:"installed"`
	// NeedsReinstall reports that Beacon's managed block uses the legacy grouped lifecycle schema,
	// which Antigravity does not run. Installed stays true so repair still rewrites the block.
	NeedsReinstall bool   `json:"needs_reinstall,omitempty"`
	BinaryPath     string `json:"binary_path,omitempty"`
	ConfigPath     string `json:"config_path,omitempty"`
	Message        string `json:"message,omitempty"`
}

type antigravityHookGroup struct {
	Matcher string               `json:"matcher,omitempty"`
	Hooks   []antigravityHookRef `json:"hooks"`
}

type antigravityHookRef struct {
	Type    string               `json:"type,omitempty"`
	Command string               `json:"command"`
	Timeout int                  `json:"timeout,omitempty"`
	Hooks   []antigravityHookRef `json:"hooks,omitempty"`
}

type antigravityHookBlock struct {
	SessionStart   []antigravityHookRef   `json:"SessionStart,omitempty"`
	PreInvocation  []antigravityHookRef   `json:"PreInvocation,omitempty"`
	PostInvocation []antigravityHookRef   `json:"PostInvocation,omitempty"`
	PreToolUse     []antigravityHookGroup `json:"PreToolUse,omitempty"`
	PostToolUse    []antigravityHookGroup `json:"PostToolUse,omitempty"`
	Stop           []antigravityHookRef   `json:"Stop,omitempty"`
}

type antigravityConfig struct {
	values map[string]json.RawMessage
}

var antigravityRuntime = hookRuntime{
	displayName: "Antigravity CLI",
	configPath:  antigravityConfigPath,
	install:     installAntigravityHooks,
	uninstall:   removeAntigravityEndpointHooks,
	isInstalled: isAntigravityInstalledAt,
}

func InstallAntigravity(opts AntigravityOptions) (AntigravityStatus, error) {
	status, err := installRuntimeHooks(antigravityRuntime, RuntimeOptions(opts))
	if err != nil {
		return AntigravityStatus{}, err
	}
	return antigravityStatusFromRuntime(status), nil
}

func UninstallAntigravity(opts AntigravityOptions) (AntigravityStatus, error) {
	status, err := uninstallRuntimeHooks(antigravityRuntime, RuntimeOptions(opts))
	if err != nil {
		return AntigravityStatus{}, err
	}
	return antigravityStatusFromRuntime(status), nil
}

func AntigravityHookStatus(opts AntigravityOptions) AntigravityStatus {
	status := antigravityStatusFromRuntime(runtimeHookStatus(antigravityRuntime, RuntimeOptions(opts)))
	if status.Installed {
		if _, legacy := antigravityManagedHookState(status.ConfigPath); legacy {
			status.NeedsReinstall = true
			status.Message = AntigravityLegacyHooksMessage(opts.Level)
		}
	}
	return status
}

// AntigravityLegacyHooksMessage explains a Beacon hook block written in the legacy grouped
// lifecycle schema and names the command that rewrites it at level.
func AntigravityLegacyHooksMessage(level Level) string {
	command := "beacon endpoint hooks install --harness antigravity"
	if level == LevelProject {
		command += " --level project"
	}
	return "Beacon's Antigravity CLI hooks use the legacy schema, which Antigravity does not run; rerun " + command + " to rewrite them"
}

func IsAntigravityInstalled(opts AntigravityOptions) bool {
	return isRuntimeInstalled(antigravityRuntime, RuntimeOptions(opts))
}

func antigravityStatusFromRuntime(status runtimeStatus) AntigravityStatus {
	return AntigravityStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		ConfigPath: status.ConfigPath,
		Message:    status.Message,
	}
}

func installAntigravityHooks(path, binaryPath, logPath, configPath string) error {
	config, err := readAntigravityConfig(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix("antigravity", binaryPath, logPath, configPath)
	// PreInvocation runs before every model call; the adapter records the turn's prompt only on
	// the first call of each turn (invocationNum 0), so it is the per-turn prompt hook.
	block := antigravityHookBlock{
		SessionStart:  []antigravityHookRef{{Type: "command", Command: prefix + " session-start"}},
		PreInvocation: []antigravityHookRef{{Type: "command", Command: prefix + " prompt-submit"}},
		PreToolUse: []antigravityHookGroup{{
			Matcher: "*",
			Hooks:   []antigravityHookRef{{Type: "command", Command: prefix + " pre-tool"}},
		}},
		PostToolUse: []antigravityHookGroup{{
			Matcher: "*",
			Hooks:   []antigravityHookRef{{Type: "command", Command: prefix + " post-tool"}},
		}},
		Stop: []antigravityHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}},
	}
	data, err := json.Marshal(block)
	if err != nil {
		return err
	}
	config.values[antigravityBeaconHookName] = data
	out, err := config.marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0600)
}

func readAntigravityConfig(path string) (antigravityConfig, error) {
	config := antigravityConfig{values: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &config.values); err != nil {
			return antigravityConfig{}, err
		}
	} else if !os.IsNotExist(err) {
		return antigravityConfig{}, err
	}
	if config.values == nil {
		config.values = map[string]json.RawMessage{}
	}
	return config, nil
}

func (config antigravityConfig) marshal() ([]byte, error) {
	if len(config.values) == 0 {
		return []byte("{}"), nil
	}
	return json.MarshalIndent(config.values, "", "  ")
}

func removeAntigravityEndpointHooks(path string) (bool, error) {
	config, err := readAntigravityConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, ok := config.values[antigravityBeaconHookName]; !ok {
		return false, nil
	}
	delete(config.values, antigravityBeaconHookName)
	if len(config.values) == 0 {
		return true, os.Remove(path)
	}
	out, err := config.marshal()
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0600)
}

func isAntigravityInstalledAt(path string) bool {
	installed, _ := antigravityManagedHookState(path)
	return installed
}

// antigravityManagedHookState reports whether Beacon's managed block names an endpoint hook, in
// either schema, and whether that block uses the legacy grouped schema Antigravity does not run.
// A legacy block still counts as installed so that repair targets and rewrites it.
func antigravityManagedHookState(path string) (installed, legacy bool) {
	config, err := readAntigravityConfig(path)
	if err != nil {
		return false, false
	}
	raw, ok := config.values[antigravityBeaconHookName]
	if !ok {
		return false, false
	}
	found, legacy := antigravityEndpointHookShape(raw)
	return found, found && legacy
}

// AntigravityConfigEndpointHooks reports whether any block of an Antigravity hooks.json document
// names a Beacon endpoint hook in the schema Antigravity runs (current), and whether one names it
// only in the legacy grouped lifecycle schema, which Antigravity does not run (legacy). The error
// is the document's parse error.
func AntigravityConfigEndpointHooks(data []byte) (current, legacy bool, err error) {
	var blocks map[string]json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		return false, false, err
	}
	for _, block := range blocks {
		found, grouped := antigravityEndpointHookShape(block)
		if !found {
			continue
		}
		if grouped {
			legacy = true
		} else {
			current = true
		}
	}
	return current, legacy, nil
}

// antigravityEndpointHookShape reports whether a hooks block names a Beacon endpoint command
// anywhere, and whether a lifecycle event in it lists {"hooks": [...]} groups where Antigravity
// expects handlers. Antigravity does not run a block in that legacy shape.
func antigravityEndpointHookShape(raw json.RawMessage) (found, legacy bool) {
	var block antigravityHookBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return false, false
	}
	for _, hooks := range [][]antigravityHookRef{
		block.SessionStart,
		block.PreInvocation,
		block.PostInvocation,
		block.Stop,
	} {
		for _, hook := range hooks {
			if hook.Hooks != nil {
				legacy = true
				for _, grouped := range hook.Hooks {
					found = found || hookcmd.IsEndpointHook(grouped.Command, "antigravity")
				}
			}
			found = found || hookcmd.IsEndpointHook(hook.Command, "antigravity")
		}
	}
	for _, groups := range [][]antigravityHookGroup{block.PreToolUse, block.PostToolUse} {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				found = found || hookcmd.IsEndpointHook(hook.Command, "antigravity")
			}
		}
	}
	return found, legacy
}

func antigravityConfigPath(level Level) (string, error) {
	switch level {
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".agents", "hooks.json"), nil
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".gemini", "config", "hooks.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
