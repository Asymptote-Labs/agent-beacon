package hooks

import (
	"fmt"
	"os"
	"path/filepath"
)

type CodexOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type CodexStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

var codexRuntime = hookRuntime{
	displayName: "Codex CLI",
	configPath:  codexHooksPath,
	install:     installCodexHooks,
	uninstall:   removeCodexEndpointHooks,
	isInstalled: isCodexInstalledAt,
}

func InstallCodex(opts CodexOptions) (CodexStatus, error) {
	status, err := installRuntimeHooks(codexRuntime, RuntimeOptions(opts))
	if err != nil {
		return CodexStatus{}, err
	}
	return codexStatusFromRuntime(status), nil
}

func UninstallCodex(opts CodexOptions) (CodexStatus, error) {
	status, err := uninstallRuntimeHooks(codexRuntime, RuntimeOptions(opts))
	if err != nil {
		return CodexStatus{}, err
	}
	return codexStatusFromRuntime(status), nil
}

func CodexHookStatus(opts CodexOptions) CodexStatus {
	return codexStatusFromRuntime(runtimeHookStatus(codexRuntime, RuntimeOptions(opts)))
}

func IsCodexInstalled(opts CodexOptions) bool {
	return isRuntimeInstalled(codexRuntime, RuntimeOptions(opts))
}

func codexStatusFromRuntime(status runtimeStatus) CodexStatus {
	return CodexStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

func installCodexHooks(path, binaryPath, logPath, configPath string) error {
	prefix := endpointCommandPrefix("codex", binaryPath, logPath, configPath)
	endpointHooks := map[string]settingsHookGroup{
		"SessionStart":     {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " inventory-heartbeat", Timeout: 10}}},
		"UserPromptSubmit": {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " inventory-heartbeat", Timeout: 10}}},
	}
	return installSettingsEndpointHooks(path, "codex", endpointHooks)
}

func removeCodexEndpointHooks(path string) (bool, error) {
	return removeSettingsEndpointHooks(path, "codex")
}

func isCodexInstalledAt(path string) bool {
	return isSettingsEndpointInstalledAt(path, "codex")
}

func codexHooksPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".codex", "hooks.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "hooks.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
