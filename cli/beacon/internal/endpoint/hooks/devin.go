package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type DevinOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type DevinDesktopOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type DevinStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type devinHookGroup struct {
	Matcher string         `json:"matcher,omitempty"`
	Hooks   []devinHookRef `json:"hooks"`
}

type devinHookRef struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type devinConfig struct {
	values     map[string]json.RawMessage
	hooks      map[string][]devinHookGroup
	standalone bool
}

var devinRuntime = hookRuntime{
	displayName: "Devin CLI",
	configPath:  devinConfigPath,
	install:     installDevinCLIHooks,
	uninstall:   removeDevinCLIEndpointHooks,
	isInstalled: isDevinCLIInstalledAt,
}

var devinDesktopRuntime = hookRuntime{
	displayName: "Devin Desktop",
	configPath:  devinConfigPath,
	install:     installDevinDesktopHooks,
	uninstall:   removeDevinDesktopEndpointHooks,
	isInstalled: isDevinDesktopInstalledAt,
}

func InstallDevin(opts DevinOptions) (DevinStatus, error) {
	status, err := installRuntimeHooks(devinRuntime, RuntimeOptions(opts))
	if err != nil {
		return DevinStatus{}, err
	}
	return devinStatusFromRuntime(status), nil
}

func UninstallDevin(opts DevinOptions) (DevinStatus, error) {
	status, err := uninstallRuntimeHooks(devinRuntime, RuntimeOptions(opts))
	if err != nil {
		return DevinStatus{}, err
	}
	return devinStatusFromRuntime(status), nil
}

func DevinHookStatus(opts DevinOptions) DevinStatus {
	return devinStatusFromRuntime(runtimeHookStatus(devinRuntime, RuntimeOptions(opts)))
}

func IsDevinInstalled(opts DevinOptions) bool {
	return isRuntimeInstalled(devinRuntime, RuntimeOptions(opts))
}

func InstallDevinDesktop(opts DevinDesktopOptions) (DevinStatus, error) {
	status, err := installRuntimeHooks(devinDesktopRuntime, RuntimeOptions(opts))
	if err != nil {
		return DevinStatus{}, err
	}
	return devinStatusFromRuntime(status), nil
}

func UninstallDevinDesktop(opts DevinDesktopOptions) (DevinStatus, error) {
	status, err := uninstallRuntimeHooks(devinDesktopRuntime, RuntimeOptions(opts))
	if err != nil {
		return DevinStatus{}, err
	}
	return devinStatusFromRuntime(status), nil
}

func DevinDesktopHookStatus(opts DevinDesktopOptions) DevinStatus {
	return devinStatusFromRuntime(runtimeHookStatus(devinDesktopRuntime, RuntimeOptions(opts)))
}

func devinStatusFromRuntime(status runtimeStatus) DevinStatus {
	return DevinStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		ConfigPath: status.ConfigPath,
		Message:    status.Message,
	}
}

func installDevinCLIHooks(path, binaryPath, logPath, configPath string) error {
	return installDevinHooksForPlatform(path, binaryPath, logPath, configPath, "devin-cli", []string{"devin", "devin-cli"})
}

func installDevinDesktopHooks(path, binaryPath, logPath, configPath string) error {
	return installDevinHooksForPlatform(path, binaryPath, logPath, configPath, "devin-desktop", []string{"devin-desktop"})
}

func installDevinHooks(path, binaryPath, logPath, configPath string) error {
	return installDevinHooksForPlatform(path, binaryPath, logPath, configPath, "devin", []string{"devin"})
}

func installDevinHooksForPlatform(path, binaryPath, logPath, configPath, platform string, replacePlatforms []string) error {
	config, err := readDevinConfig(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix(platform, binaryPath, logPath, configPath)
	endpointHooks := map[string]devinHookGroup{
		"SessionStart":      {Hooks: []devinHookRef{{Type: "command", Command: prefix + " session-start"}}},
		"UserPromptSubmit":  {Hooks: []devinHookRef{{Type: "command", Command: prefix + " prompt-submit", Timeout: 30}}},
		"PreToolUse":        {Matcher: "", Hooks: []devinHookRef{{Type: "command", Command: prefix + " pre-tool"}}},
		"PermissionRequest": {Matcher: "", Hooks: []devinHookRef{{Type: "command", Command: prefix + " permission-request"}}},
		"PostToolUse":       {Matcher: "", Hooks: []devinHookRef{{Type: "command", Command: prefix + " post-tool"}}},
		"Stop":              {Hooks: []devinHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}}},
		"SessionEnd":        {Hooks: []devinHookRef{{Type: "command", Command: prefix + " session-end"}}},
	}
	for eventName, group := range endpointHooks {
		config.hooks[eventName] = mergeDevinEndpointHook(config.hooks[eventName], group, replacePlatforms...)
	}
	data, err := config.marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func readDevinConfig(path string) (devinConfig, error) {
	config := devinConfig{
		values:     map[string]json.RawMessage{},
		hooks:      map[string][]devinHookGroup{},
		standalone: filepath.Base(path) == "hooks.v1.json",
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if config.standalone {
			if err := json.Unmarshal(data, &config.hooks); err != nil {
				return devinConfig{}, err
			}
		} else {
			if err := json.Unmarshal(data, &config.values); err != nil {
				return devinConfig{}, err
			}
			if rawHooks, ok := config.values["hooks"]; ok {
				if err := json.Unmarshal(rawHooks, &config.hooks); err != nil {
					return devinConfig{}, err
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return devinConfig{}, err
	}
	if config.hooks == nil {
		config.hooks = map[string][]devinHookGroup{}
	}
	return config, nil
}

func (config devinConfig) marshal() ([]byte, error) {
	if config.standalone {
		if len(config.hooks) == 0 {
			return []byte("{}"), nil
		}
		return json.MarshalIndent(config.hooks, "", "  ")
	}
	out := make(map[string]json.RawMessage, len(config.values)+1)
	for key, value := range config.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(config.hooks) > 0 {
		data, err := json.Marshal(config.hooks)
		if err != nil {
			return nil, err
		}
		out["hooks"] = data
	}
	return json.MarshalIndent(out, "", "  ")
}

func mergeDevinEndpointHook(existing []devinHookGroup, group devinHookGroup, platforms ...string) []devinHookGroup {
	out := make([]devinHookGroup, 0, len(existing)+1)
	for _, item := range existing {
		filtered, changed := filterDevinEndpointHooks(item, platforms...)
		if !changed || len(filtered.Hooks) > 0 {
			out = append(out, item)
			if changed {
				out[len(out)-1] = filtered
			}
		}
	}
	return append(out, group)
}

func removeDevinEndpointHooks(path string) (bool, error) {
	return removeDevinEndpointHooksForPlatforms(path, "devin")
}

func removeDevinCLIEndpointHooks(path string) (bool, error) {
	return removeDevinEndpointHooksForPlatforms(path, "devin", "devin-cli")
}

func removeDevinDesktopEndpointHooks(path string) (bool, error) {
	return removeDevinEndpointHooksForPlatforms(path, "devin-desktop")
}

func removeDevinEndpointHooksForPlatforms(path string, platforms ...string) (bool, error) {
	config, err := readDevinConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	changed := false
	for eventName, groups := range config.hooks {
		filtered := groups[:0]
		for _, group := range groups {
			withoutEndpointHooks, groupChanged := filterDevinEndpointHooks(group, platforms...)
			if groupChanged {
				changed = true
			}
			if len(withoutEndpointHooks.Hooks) == 0 {
				continue
			}
			filtered = append(filtered, withoutEndpointHooks)
		}
		if len(filtered) == 0 {
			delete(config.hooks, eventName)
		} else {
			config.hooks[eventName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	if config.standalone && len(config.hooks) == 0 {
		return true, os.Remove(path)
	}
	out, err := config.marshal()
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0600)
}

func isDevinEndpointHookGroup(group devinHookGroup, platforms ...string) bool {
	for _, hook := range group.Hooks {
		if isDevinEndpointHookCommand(hook.Command, platforms...) {
			return true
		}
	}
	return false
}

func filterDevinEndpointHooks(group devinHookGroup, platforms ...string) (devinHookGroup, bool) {
	filtered := group
	filtered.Hooks = group.Hooks[:0]
	changed := false
	for _, hook := range group.Hooks {
		if isDevinEndpointHookCommand(hook.Command, platforms...) {
			changed = true
			continue
		}
		filtered.Hooks = append(filtered.Hooks, hook)
	}
	return filtered, changed
}

func isDevinInstalledAt(path string) bool {
	return isDevinInstalledAtPlatforms(path, "devin")
}

func isDevinCLIInstalledAt(path string) bool {
	return isDevinInstalledAtPlatforms(path, "devin", "devin-cli")
}

func isDevinDesktopInstalledAt(path string) bool {
	return isDevinInstalledAtPlatforms(path, "devin-desktop")
}

func isDevinInstalledAtPlatforms(path string, platforms ...string) bool {
	config, err := readDevinConfig(path)
	if err != nil {
		return false
	}
	for _, groups := range config.hooks {
		for _, group := range groups {
			if isDevinEndpointHookGroup(group, platforms...) {
				return true
			}
		}
	}
	return false
}

func isDevinEndpointHookCommand(command string, platforms ...string) bool {
	if len(platforms) == 0 {
		platforms = []string{"devin"}
	}
	for _, platform := range platforms {
		if isEndpointHookCommand(command, platform) {
			return true
		}
	}
	return false
}

func devinConfigPath(level Level) (string, error) {
	switch level {
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".devin", "hooks.v1.json"), nil
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "devin", "config.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
