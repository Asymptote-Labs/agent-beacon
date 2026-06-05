package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type HermesOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type HermesStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type hermesConfig struct {
	values map[string]interface{}
	hooks  map[string][]hermesHookRef
}

type hermesHookRef struct {
	Matcher string `yaml:"matcher,omitempty"`
	Command string `yaml:"command"`
	Timeout int    `yaml:"timeout,omitempty"`
}

var hermesRuntime = hookRuntime{
	displayName: "Hermes Agent",
	configPath:  hermesConfigPath,
	install:     installHermesConfig,
	uninstall:   removeHermesEndpointHooks,
	isInstalled: isHermesInstalledAt,
}

func InstallHermes(opts HermesOptions) (HermesStatus, error) {
	status, err := installRuntimeHooks(hermesRuntime, RuntimeOptions(opts))
	if err != nil {
		return HermesStatus{}, err
	}
	return hermesStatusFromRuntime(status), nil
}

func UninstallHermes(opts HermesOptions) (HermesStatus, error) {
	status, err := uninstallRuntimeHooks(hermesRuntime, RuntimeOptions(opts))
	if err != nil {
		return HermesStatus{}, err
	}
	return hermesStatusFromRuntime(status), nil
}

func HermesHookStatus(opts HermesOptions) HermesStatus {
	return hermesStatusFromRuntime(runtimeHookStatus(hermesRuntime, RuntimeOptions(opts)))
}

func IsHermesInstalled(opts HermesOptions) bool {
	return isRuntimeInstalled(hermesRuntime, RuntimeOptions(opts))
}

func hermesStatusFromRuntime(status runtimeStatus) HermesStatus {
	return HermesStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		ConfigPath: status.ConfigPath,
		Message:    status.Message,
	}
}

func installHermesConfig(path, binaryPath, logPath, configPath string) error {
	config, err := readHermesConfig(path)
	if err != nil {
		return err
	}
	prefix := hermesEndpointCommandPrefix(binaryPath, logPath, configPath)
	endpointHooks := map[string]hermesHookRef{
		"on_session_start":       {Command: prefix + " session-start"},
		"pre_llm_call":           {Command: prefix + " prompt-submit", Timeout: 30},
		"pre_tool_call":          {Matcher: ".*", Command: prefix + " pre-tool", Timeout: 10},
		"post_tool_call":         {Matcher: ".*", Command: prefix + " post-tool", Timeout: 10},
		"pre_approval_request":   {Command: prefix + " permission-request", Timeout: 10},
		"post_approval_response": {Command: prefix + " permission-request", Timeout: 10},
		"subagent_stop":          {Command: prefix + " subagent-stop", Timeout: 10},
		"on_session_end":         {Command: prefix + " session-end"},
		"on_session_finalize":    {Command: prefix + " session-end"},
	}
	for eventName, ref := range endpointHooks {
		config.hooks[eventName] = mergeHermesEndpointHook(config.hooks[eventName], ref)
	}
	return writeHermesConfig(path, config)
}

func hermesEndpointCommandPrefix(binaryPath, logPath, configPath string) string {
	return fmt.Sprintf("env BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG=%s BEACON_ENDPOINT_CONFIG=%s %s --platform hermes", shellQuote(logPath), shellQuote(configPath), shellQuote(binaryPath))
}

func readHermesConfig(path string) (hermesConfig, error) {
	config := hermesConfig{
		values: map[string]interface{}{},
		hooks:  map[string][]hermesHookRef{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 0 {
			if err := yaml.Unmarshal(data, &config.values); err != nil {
				return hermesConfig{}, err
			}
		}
	} else if !os.IsNotExist(err) {
		return hermesConfig{}, err
	}
	if config.values == nil {
		config.values = map[string]interface{}{}
	}
	if rawHooks, ok := config.values["hooks"]; ok && rawHooks != nil {
		data, err := yaml.Marshal(rawHooks)
		if err != nil {
			return hermesConfig{}, err
		}
		if err := yaml.Unmarshal(data, &config.hooks); err != nil {
			return hermesConfig{}, err
		}
	}
	if config.hooks == nil {
		config.hooks = map[string][]hermesHookRef{}
	}
	return config, nil
}

func writeHermesConfig(path string, config hermesConfig) error {
	out := make(map[string]interface{}, len(config.values)+1)
	for key, value := range config.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(config.hooks) > 0 {
		out["hooks"] = config.hooks
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func mergeHermesEndpointHook(existing []hermesHookRef, ref hermesHookRef) []hermesHookRef {
	out := make([]hermesHookRef, 0, len(existing)+1)
	for _, item := range existing {
		if !isEndpointHookCommand(item.Command, "hermes") {
			out = append(out, item)
		}
	}
	return append(out, ref)
}

func removeHermesEndpointHooks(path string) (bool, error) {
	config, err := readHermesConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	changed := false
	for eventName, refs := range config.hooks {
		filtered := refs[:0]
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "hermes") {
				changed = true
				continue
			}
			filtered = append(filtered, ref)
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
	if len(config.hooks) == 0 {
		delete(config.values, "hooks")
	}
	if len(config.values) == 0 && len(config.hooks) == 0 {
		return true, os.Remove(path)
	}
	return true, writeHermesConfig(path, config)
}

func isHermesInstalledAt(path string) bool {
	config, err := readHermesConfig(path)
	if err != nil {
		return false
	}
	for _, refs := range config.hooks {
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "hermes") {
				return true
			}
		}
	}
	return false
}

func hermesConfigPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".hermes", "config.yaml"), nil
	case LevelProject:
		return "", fmt.Errorf("Hermes Agent endpoint hooks support user-level config only")
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
