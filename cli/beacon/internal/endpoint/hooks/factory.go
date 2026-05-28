package hooks

import (
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

type factoryHookGroup = settingsHookGroup
type factoryHookRef = settingsHookRef
type factorySettings = settingsHooksFile

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
	prefix := endpointCommandPrefix("factory", binaryPath, logPath, configPath)
	endpointHooks := map[string]factoryHookGroup{
		"SessionStart":     {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " session-start"}}},
		"UserPromptSubmit": {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " prompt-submit", Timeout: 30}}},
		"PostToolUse":      {Matcher: "Write|Edit|MultiEdit|Create", Hooks: []factoryHookRef{{Type: "command", Command: prefix + " post-tool"}}},
		"Stop":             {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}}},
		"SessionEnd":       {Hooks: []factoryHookRef{{Type: "command", Command: prefix + " session-end"}}},
	}
	return installSettingsEndpointHooks(path, "factory", endpointHooks)
}

func readFactorySettings(path string) (factorySettings, error) {
	return readSettingsHooks(path)
}

func removeFactoryEndpointHooks(path string) (bool, error) {
	return removeSettingsEndpointHooks(path, "factory")
}

func isFactoryInstalledAt(path string) bool {
	return isSettingsEndpointInstalledAt(path, "factory")
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
