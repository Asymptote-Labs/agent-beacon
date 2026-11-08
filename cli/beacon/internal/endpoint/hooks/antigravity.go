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
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type antigravityHookGroup struct {
	Matcher string               `json:"matcher,omitempty"`
	Hooks   []antigravityHookRef `json:"hooks"`
}

type antigravityHookRef struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
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
	return antigravityStatusFromRuntime(runtimeHookStatus(antigravityRuntime, RuntimeOptions(opts)))
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
	block := antigravityHookBlock{
		SessionStart: []antigravityHookRef{{Type: "command", Command: prefix + " session-start"}},
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
	config, err := readAntigravityConfig(path)
	if err != nil {
		return false
	}
	raw, ok := config.values[antigravityBeaconHookName]
	if !ok {
		return false
	}
	return hasAntigravityEndpointHook(raw)
}

func AntigravityConfigHasEndpointHook(data []byte) (bool, error) {
	var blocks map[string]json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		return false, err
	}
	for _, block := range blocks {
		if hasAntigravityEndpointHook(block) {
			return true, nil
		}
	}
	return false, nil
}

func hasAntigravityEndpointHook(raw json.RawMessage) bool {
	var block antigravityHookBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return false
	}
	handlers := [][]antigravityHookRef{
		block.SessionStart,
		block.PreInvocation,
		block.PostInvocation,
		block.Stop,
	}
	for _, groups := range [][]antigravityHookGroup{block.PreToolUse, block.PostToolUse} {
		for _, group := range groups {
			handlers = append(handlers, group.Hooks)
		}
	}
	found := false
	for _, hooks := range handlers {
		for _, hook := range hooks {
			if hook.Command == "" {
				return false
			}
			if hookcmd.IsEndpointHook(hook.Command, "antigravity") {
				found = true
			}
		}
	}
	return found
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
