package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type FactoryOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type FactoryStatus struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

type factoryHookGroup struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []factoryHookRef `json:"hooks"`
}

type factoryHookRef struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type factorySettings struct {
	values map[string]json.RawMessage
	hooks  map[string][]factoryHookGroup
}

var factoryRuntime = hookRuntime{
	displayName: "Factory",
	configPath:  factorySettingsPath,
	install:     installFactorySettings,
	uninstall:   removeFactoryEndpointHooks,
	isInstalled: isFactoryInstalledAt,
}

func InstallFactory(opts FactoryOptions) (FactoryStatus, error) {
	status, err := installRuntimeHooks(factoryRuntime, RuntimeOptions(opts))
	if err != nil {
		return FactoryStatus{}, err
	}
	return factoryStatusFromRuntime(status), nil
}

func UninstallFactory(opts FactoryOptions) (FactoryStatus, error) {
	status, err := uninstallRuntimeHooks(factoryRuntime, RuntimeOptions(opts))
	if err != nil {
		return FactoryStatus{}, err
	}
	return factoryStatusFromRuntime(status), nil
}

func FactoryHookStatus(opts FactoryOptions) FactoryStatus {
	return factoryStatusFromRuntime(runtimeHookStatus(factoryRuntime, RuntimeOptions(opts)))
}

func IsFactoryInstalled(opts FactoryOptions) bool {
	return isRuntimeInstalled(factoryRuntime, RuntimeOptions(opts))
}

func factoryStatusFromRuntime(status runtimeStatus) FactoryStatus {
	return FactoryStatus{
		Installed:    status.Installed,
		BinaryPath:   status.BinaryPath,
		SettingsPath: status.ConfigPath,
		Message:      status.Message,
	}
}

func installFactorySettings(path, binaryPath, logPath, configPath string) error {
	settings, err := readFactorySettings(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix("factory", binaryPath, logPath, configPath)
	endpointHooks := map[string]factoryHookGroup{
		"SessionStart":     {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " session-start"}}},
		"UserPromptSubmit": {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " prompt-submit", Timeout: 30}}},
		"PostToolUse":      {Matcher: "Write|Edit|MultiEdit|Create", Hooks: []factoryHookRef{{Type: "command", Command: prefix + " post-tool"}}},
		"Stop":             {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}}},
		"SessionEnd":       {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " session-end"}}},
	}
	for eventName, group := range endpointHooks {
		settings.hooks[eventName] = mergeFactoryEndpointHook(settings.hooks[eventName], group)
	}
	data, err := settings.marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func readFactorySettings(path string) (factorySettings, error) {
	settings := factorySettings{
		values: map[string]json.RawMessage{},
		hooks:  map[string][]factoryHookGroup{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &settings.values); err != nil {
			return factorySettings{}, err
		}
		if rawHooks, ok := settings.values["hooks"]; ok {
			if err := json.Unmarshal(rawHooks, &settings.hooks); err != nil {
				return factorySettings{}, err
			}
		}
	} else if !os.IsNotExist(err) {
		return factorySettings{}, err
	}
	if settings.hooks == nil {
		settings.hooks = map[string][]factoryHookGroup{}
	}
	return settings, nil
}

func (settings factorySettings) marshal() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(settings.values)+1)
	for key, value := range settings.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(settings.hooks) > 0 {
		data, err := json.Marshal(settings.hooks)
		if err != nil {
			return nil, err
		}
		out["hooks"] = data
	}
	return json.MarshalIndent(out, "", "  ")
}

func mergeFactoryEndpointHook(existing []factoryHookGroup, group factoryHookGroup) []factoryHookGroup {
	out := make([]factoryHookGroup, 0, len(existing)+1)
	for _, item := range existing {
		filtered, changed := filterFactoryEndpointHooks(item)
		if !changed || len(filtered.Hooks) > 0 {
			out = append(out, item)
			if changed {
				out[len(out)-1] = filtered
			}
		}
	}
	return append(out, group)
}

func removeFactoryEndpointHooks(path string) (bool, error) {
	settings, err := readFactorySettings(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	changed := false
	for eventName, groups := range settings.hooks {
		filtered := groups[:0]
		for _, group := range groups {
			withoutEndpointHooks, groupChanged := filterFactoryEndpointHooks(group)
			if groupChanged {
				changed = true
			}
			if len(withoutEndpointHooks.Hooks) == 0 {
				continue
			}
			filtered = append(filtered, withoutEndpointHooks)
		}
		if len(filtered) == 0 {
			delete(settings.hooks, eventName)
		} else {
			settings.hooks[eventName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	out, err := settings.marshal()
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0600)
}

func isFactoryEndpointHookGroup(group factoryHookGroup) bool {
	for _, hook := range group.Hooks {
		if isEndpointHookCommand(hook.Command, "factory") {
			return true
		}
	}
	return false
}

func filterFactoryEndpointHooks(group factoryHookGroup) (factoryHookGroup, bool) {
	filtered := group
	filtered.Hooks = group.Hooks[:0]
	changed := false
	for _, hook := range group.Hooks {
		if isEndpointHookCommand(hook.Command, "factory") {
			changed = true
			continue
		}
		filtered.Hooks = append(filtered.Hooks, hook)
	}
	return filtered, changed
}

func isFactoryInstalledAt(path string) bool {
	settings, err := readFactorySettings(path)
	if err != nil {
		return false
	}
	for _, groups := range settings.hooks {
		for _, group := range groups {
			if isFactoryEndpointHookGroup(group) {
				return true
			}
		}
	}
	return false
}

func factorySettingsPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".factory", "settings.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".factory", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
