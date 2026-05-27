package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type VSCodeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type VSCodeStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type vscodeHookRef struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type vscodeHooksFile struct {
	Hooks map[string][]vscodeHookRef `json:"hooks"`
}

var vscodeRuntime = hookRuntime{
	displayName: "VS Code",
	configPath:  vscodeHooksPath,
	install:     installVSCodeHooks,
	uninstall:   removeVSCodeEndpointHooks,
	isInstalled: isVSCodeInstalledAt,
}

func InstallVSCode(opts VSCodeOptions) (VSCodeStatus, error) {
	status, err := installRuntimeHooks(vscodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return VSCodeStatus{}, err
	}
	return vscodeStatusFromRuntime(status), nil
}

func UninstallVSCode(opts VSCodeOptions) (VSCodeStatus, error) {
	status, err := uninstallRuntimeHooks(vscodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return VSCodeStatus{}, err
	}
	return vscodeStatusFromRuntime(status), nil
}

func VSCodeHookStatus(opts VSCodeOptions) VSCodeStatus {
	return vscodeStatusFromRuntime(runtimeHookStatus(vscodeRuntime, RuntimeOptions(opts)))
}

func vscodeStatusFromRuntime(status runtimeStatus) VSCodeStatus {
	return VSCodeStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

func installVSCodeHooks(path, binaryPath, logPath, configPath string) error {
	hooksFile, err := readVSCodeHooks(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix("vscode", binaryPath, logPath, configPath)
	endpointHooks := map[string]vscodeHookRef{
		"SessionStart":     {Type: "command", Command: prefix + " session-start"},
		"UserPromptSubmit": {Type: "command", Command: prefix + " prompt-submit", Timeout: 30},
		"PreToolUse":       {Type: "command", Command: prefix + " pre-tool"},
		"PostToolUse":      {Type: "command", Command: prefix + " post-tool"},
		"SubagentStart":    {Type: "command", Command: prefix + " subagent-start"},
		"SubagentStop":     {Type: "command", Command: prefix + " subagent-stop"},
		"Stop":             {Type: "command", Command: prefix + " stop", Timeout: 45},
	}
	for eventName, ref := range endpointHooks {
		hooksFile.Hooks[eventName] = mergeVSCodeEndpointHook(hooksFile.Hooks[eventName], ref)
	}
	data, err := json.MarshalIndent(hooksFile, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func readVSCodeHooks(path string) (vscodeHooksFile, error) {
	hooksFile := vscodeHooksFile{Hooks: map[string][]vscodeHookRef{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &hooksFile); err != nil {
			return vscodeHooksFile{}, err
		}
	} else if !os.IsNotExist(err) {
		return vscodeHooksFile{}, err
	}
	if hooksFile.Hooks == nil {
		hooksFile.Hooks = map[string][]vscodeHookRef{}
	}
	return hooksFile, nil
}

func mergeVSCodeEndpointHook(existing []vscodeHookRef, ref vscodeHookRef) []vscodeHookRef {
	out := make([]vscodeHookRef, 0, len(existing)+1)
	for _, item := range existing {
		if !isEndpointHookCommand(item.Command, "vscode") {
			out = append(out, item)
		}
	}
	return append(out, ref)
}

func removeVSCodeEndpointHooks(path string) (bool, error) {
	hooksFile, err := readVSCodeHooks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	changed := false
	for eventName, refs := range hooksFile.Hooks {
		filtered := refs[:0]
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "vscode") {
				changed = true
				continue
			}
			filtered = append(filtered, ref)
		}
		if len(filtered) == 0 {
			delete(hooksFile.Hooks, eventName)
		} else {
			hooksFile.Hooks[eventName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	if len(hooksFile.Hooks) == 0 {
		return true, os.Remove(path)
	}
	data, err := json.MarshalIndent(hooksFile, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, data, 0600)
}

func isVSCodeInstalledAt(path string) bool {
	hooksFile, err := readVSCodeHooks(path)
	if err != nil {
		return false
	}
	for _, refs := range hooksFile.Hooks {
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "vscode") {
				return true
			}
		}
	}
	return false
}

func vscodeHooksPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".copilot", "hooks", "beacon.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".github", "hooks", "beacon.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
