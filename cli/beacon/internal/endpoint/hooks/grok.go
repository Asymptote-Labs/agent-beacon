package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	grokManagedHookFileName = "beacon-endpoint.json"
	grokManagedHookMarker   = "beacon-managed-grok-hooks:v1"
)

type GrokOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type GrokStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type grokHooksFile struct {
	Description string                     `json:"description,omitempty"`
	Beacon      string                     `json:"beacon,omitempty"`
	Hooks       map[string][]grokHookGroup `json:"hooks"`
}

type grokHookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []grokHookRef `json:"hooks"`
}

type grokHookRef struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Timeout int               `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

var grokRuntime = hookRuntime{
	displayName: "Grok",
	configPath:  grokHooksPath,
	install:     installGrokHooks,
	uninstall:   removeGrokHooks,
	isInstalled: isGrokInstalledAt,
}

func InstallGrok(opts GrokOptions) (GrokStatus, error) {
	status, err := installRuntimeHooks(grokRuntime, RuntimeOptions(opts))
	if err != nil {
		return GrokStatus{}, err
	}
	return grokProjectTrustMessage(grokStatusFromRuntime(status), opts.Level), nil
}

func UninstallGrok(opts GrokOptions) (GrokStatus, error) {
	status, err := uninstallRuntimeHooks(grokRuntime, RuntimeOptions(opts))
	if err != nil {
		return GrokStatus{}, err
	}
	return grokStatusFromRuntime(status), nil
}

func GrokHookStatus(opts GrokOptions) GrokStatus {
	return grokProjectTrustMessage(grokStatusFromRuntime(runtimeHookStatus(grokRuntime, RuntimeOptions(opts))), opts.Level)
}

func IsGrokInstalled(opts GrokOptions) bool {
	return isRuntimeInstalled(grokRuntime, RuntimeOptions(opts))
}

func grokStatusFromRuntime(status runtimeStatus) GrokStatus {
	return GrokStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

func installGrokHooks(path, binaryPath, logPath, configPath string) error {
	prefix := endpointCommandPrefix("grok", binaryPath, logPath, configPath)
	hooks := grokHooksFile{
		Description: "Beacon managed Grok endpoint telemetry hooks.",
		Beacon:      grokManagedHookMarker,
		Hooks: map[string][]grokHookGroup{
			"SessionStart":     {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " session-start"}}}},
			"UserPromptSubmit": {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " prompt-submit", Timeout: 30}}}},
			"PreToolUse":       {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " pre-tool", Timeout: 10}}}},
			"PostToolUse":      {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " post-tool", Timeout: 10}}}},
			"PostToolUseFailure": {
				{Hooks: []grokHookRef{{Type: "command", Command: prefix + " post-tool", Timeout: 10}}},
			},
			"Stop":       {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}}}},
			"SessionEnd": {{Hooks: []grokHookRef{{Type: "command", Command: prefix + " session-end"}}}},
		},
	}
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func removeGrokHooks(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !isGrokManagedHookFile(data) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isGrokInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return isGrokManagedHookFile(data)
}

func isGrokManagedHookFile(data []byte) bool {
	var hooks grokHooksFile
	if err := json.Unmarshal(data, &hooks); err != nil {
		return false
	}
	if hooks.Beacon != grokManagedHookMarker {
		return false
	}
	for _, groups := range hooks.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if isEndpointHookCommand(hook.Command, "grok") {
					return true
				}
			}
		}
	}
	return false
}

func grokHooksPath(level Level) (string, error) {
	dir, err := grokHooksDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, grokManagedHookFileName), nil
}

func grokHooksDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".grok", "hooks"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".grok", "hooks"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

func grokProjectTrustMessage(status GrokStatus, level Level) GrokStatus {
	if level == LevelProject && status.Message != "" && !strings.Contains(status.Message, "/hooks-trust") {
		status.Message += "; run /hooks-trust in Grok before project hooks execute"
	}
	return status
}
