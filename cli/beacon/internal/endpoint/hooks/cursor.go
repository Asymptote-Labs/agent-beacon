package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/embedded"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
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

func InstallCursor(opts CursorOptions) (CursorStatus, error) {
	if !embedded.HasEmbeddedBinary() {
		return CursorStatus{}, fmt.Errorf("no hooks binary embedded")
	}
	if opts.LogPath == "" {
		opts.LogPath = defaultLogPath(opts.UserMode)
	}
	binaryPath, err := writeEndpointHookBinary(opts.UserMode)
	if err != nil {
		return CursorStatus{}, err
	}
	targetDir, err := cursorTargetDir(opts.Level)
	if err != nil {
		return CursorStatus{}, err
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return CursorStatus{}, err
	}
	hooksPath := filepath.Join(targetDir, "hooks.json")
	if err := installCursorHooksJSON(hooksPath, binaryPath, opts.LogPath); err != nil {
		return CursorStatus{}, err
	}
	return CursorStatus{Installed: true, BinaryPath: binaryPath, HooksJSONPath: hooksPath, Message: "Cursor endpoint hooks installed"}, nil
}

func UninstallCursor(opts CursorOptions) (CursorStatus, error) {
	targetDir, err := cursorTargetDir(opts.Level)
	if err != nil {
		return CursorStatus{}, err
	}
	hooksPath := filepath.Join(targetDir, "hooks.json")
	updated, err := removeEndpointHooks(hooksPath)
	if err != nil {
		return CursorStatus{}, err
	}
	status := CursorStatus{HooksJSONPath: hooksPath, Message: "Cursor endpoint hooks were not present"}
	if updated {
		status.Message = "Cursor endpoint hooks removed"
	}
	status.Installed = IsCursorInstalled(opts)
	return status, nil
}

func CursorHookStatus(opts CursorOptions) CursorStatus {
	targetDir, err := cursorTargetDir(opts.Level)
	if err != nil {
		return CursorStatus{Message: err.Error()}
	}
	hooksPath := filepath.Join(targetDir, "hooks.json")
	status := CursorStatus{HooksJSONPath: hooksPath}
	status.Installed = IsCursorInstalled(opts)
	if status.Installed {
		status.Message = "Cursor endpoint hooks are installed"
	} else {
		status.Message = "Cursor endpoint hooks are not installed"
	}
	if path, err := endpointHookBinaryPath(opts.UserMode); err == nil {
		status.BinaryPath = path
	}
	return status
}

func IsCursorInstalled(opts CursorOptions) bool {
	targetDir, err := cursorTargetDir(opts.Level)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(targetDir, "hooks.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "BEACON_ENDPOINT_MODE=1") && strings.Contains(string(data), "beacon-hooks")
}

func installCursorHooksJSON(path, binaryPath, logPath string) error {
	hooksJSON, err := readHooksJSON(path)
	if err != nil {
		return err
	}
	commandPrefix := fmt.Sprintf("BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG=%s %s --platform cursor", shellQuote(logPath), shellQuote(binaryPath))
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
		_ = json.Unmarshal(data, &hooksJSON)
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
	return strings.Contains(command, "BEACON_ENDPOINT_MODE=1") || strings.Contains(command, "beacon-hooks")
}

func writeEndpointHookBinary(userMode bool) (string, error) {
	path, err := endpointHookBinaryPath(userMode)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	_ = os.Remove(path)
	return path, os.WriteFile(path, embedded.HooksBinary, 0755)
}

func endpointHookBinaryPath(userMode bool) (string, error) {
	base := endpointconfig.BaseDir(userMode)
	return filepath.Join(base, "hooks", embedded.GetBinaryName()), nil
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

func defaultLogPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
		}
	}
	return "/var/log/beacon-agent/runtime.jsonl"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
