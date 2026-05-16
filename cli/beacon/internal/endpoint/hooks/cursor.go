package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Level string

const (
	LevelUser    Level = "user"
	LevelProject Level = "project"
)

type HookRef struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type HooksJSON struct {
	Version int                  `json:"version"`
	Hooks   map[string][]HookRef `json:"hooks"`
}

type CursorOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type CursorStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	HooksJSONPath string `json:"hooks_json_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var cursorRuntime = hookRuntime{
	displayName: "Cursor",
	configPath:  cursorHooksJSONPath,
	install:     installCursorHooksJSON,
	uninstall:   removeEndpointHooks,
	isInstalled: isCursorInstalledAt,
}

func InstallCursor(opts CursorOptions) (CursorStatus, error) {
	status, err := installRuntimeHooks(cursorRuntime, RuntimeOptions(opts))
	if err != nil {
		return CursorStatus{}, err
	}
	return cursorStatusFromRuntime(status), nil
}

func UninstallCursor(opts CursorOptions) (CursorStatus, error) {
	status, err := uninstallRuntimeHooks(cursorRuntime, RuntimeOptions(opts))
	if err != nil {
		return CursorStatus{}, err
	}
	return cursorStatusFromRuntime(status), nil
}

func CursorHookStatus(opts CursorOptions) CursorStatus {
	return cursorStatusFromRuntime(runtimeHookStatus(cursorRuntime, RuntimeOptions(opts)))
}

func IsCursorInstalled(opts CursorOptions) bool {
	return isRuntimeInstalled(cursorRuntime, RuntimeOptions(opts))
}

func cursorStatusFromRuntime(status runtimeStatus) CursorStatus {
	return CursorStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		HooksJSONPath: status.ConfigPath,
		Message:       status.Message,
	}
}

func installCursorHooksJSON(path, binaryPath, logPath, configPath string) error {
	hooksJSON, err := readHooksJSON(path)
	if err != nil {
		return err
	}
	commandPrefix := endpointCommandPrefix("cursor", binaryPath, logPath, configPath)
	endpointHooks := map[string]HookRef{
		"sessionStart":       {Command: commandPrefix + " session-start"},
		"beforeSubmitPrompt": {Command: commandPrefix + " prompt-submit", Timeout: 30},
		"preToolUse":         {Command: commandPrefix + " pre-tool", Matcher: "Write|Edit|MultiEdit|Shell|MCP"},
		"afterFileEdit":      {Command: commandPrefix + " post-tool"},
		"postToolUse":        {Command: commandPrefix + " post-tool"},
		"postToolUseFailure": {Command: commandPrefix + " post-tool"},
		"stop":               {Command: commandPrefix + " stop"},
		"sessionEnd":         {Command: commandPrefix + " session-end"},
	}
	for hookName, ref := range endpointHooks {
		hooksJSON.Hooks[hookName] = mergeEndpointHook(hooksJSON.Hooks[hookName], ref)
	}
	data, err := json.MarshalIndent(hooksJSON, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func readHooksJSON(path string) (HooksJSON, error) {
	var hooksJSON HooksJSON
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &hooksJSON); err != nil {
			return HooksJSON{}, err
		}
	} else if !os.IsNotExist(err) {
		return HooksJSON{}, err
	}
	if hooksJSON.Version == 0 {
		hooksJSON.Version = 1
	}
	if hooksJSON.Hooks == nil {
		hooksJSON.Hooks = map[string][]HookRef{}
	}
	return hooksJSON, nil
}

func mergeEndpointHook(existing []HookRef, ref HookRef) []HookRef {
	out := make([]HookRef, 0, len(existing)+1)
	for _, item := range existing {
		if !isEndpointHook(item.Command) {
			out = append(out, item)
		}
	}
	return append(out, ref)
}

func removeEndpointHooks(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var hooksJSON HooksJSON
	if err := json.Unmarshal(data, &hooksJSON); err != nil {
		return false, err
	}
	changed := false
	for hookName, refs := range hooksJSON.Hooks {
		filtered := refs[:0]
		for _, ref := range refs {
			if isEndpointHook(ref.Command) {
				changed = true
				continue
			}
			filtered = append(filtered, ref)
		}
		if len(filtered) == 0 {
			delete(hooksJSON.Hooks, hookName)
		} else {
			hooksJSON.Hooks[hookName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	if len(hooksJSON.Hooks) == 0 {
		return true, os.Remove(path)
	}
	out, err := json.MarshalIndent(hooksJSON, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0644)
}

func isEndpointHook(command string) bool {
	return isEndpointHookCommand(command, "cursor")
}

func isCursorInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var hooksJSON HooksJSON
	if err := json.Unmarshal(data, &hooksJSON); err != nil {
		return false
	}
	for _, refs := range hooksJSON.Hooks {
		for _, ref := range refs {
			if isEndpointHook(ref.Command) {
				return true
			}
		}
	}
	return false
}

func cursorHooksJSONPath(level Level) (string, error) {
	targetDir, err := cursorTargetDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(targetDir, "hooks.json"), nil
}

func cursorTargetDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cursor"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".cursor"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
