package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	opencodeplugin "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/opencode"
)

const (
	opencodePluginFileName      = "beacon.ts"
	opencodeManagedPluginMarker = "beacon-managed-opencode-plugin:v1"
)

var opencodePluginTemplate = opencodeplugin.Template

type OpenCodeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OpenCodeStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	PluginPath string `json:"plugin_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

var opencodeRuntime = hookRuntime{
	displayName: "opencode",
	configPath:  opencodePluginPath,
	install:     installOpenCodePlugin,
	uninstall:   removeOpenCodePlugin,
	isInstalled: isOpenCodeInstalledAt,
}

func InstallOpenCode(opts OpenCodeOptions) (OpenCodeStatus, error) {
	status, err := installRuntimeHooks(opencodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenCodeStatus{}, err
	}
	return opencodeStatusFromRuntime(status), nil
}

func UninstallOpenCode(opts OpenCodeOptions) (OpenCodeStatus, error) {
	status, err := uninstallRuntimeHooks(opencodeRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenCodeStatus{}, err
	}
	return opencodeStatusFromRuntime(status), nil
}

func OpenCodeHookStatus(opts OpenCodeOptions) OpenCodeStatus {
	return opencodeStatusFromRuntime(runtimeHookStatus(opencodeRuntime, RuntimeOptions(opts)))
}

func IsOpenCodeInstalled(opts OpenCodeOptions) bool {
	return isRuntimeInstalled(opencodeRuntime, RuntimeOptions(opts))
}

func opencodeStatusFromRuntime(status runtimeStatus) OpenCodeStatus {
	out := OpenCodeStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		PluginPath: status.ConfigPath,
		Message:    status.Message,
	}
	if out.Installed && out.BinaryPath != "" {
		if _, err := os.Stat(out.BinaryPath); err != nil {
			out.Installed = false
			out.Message = fmt.Sprintf("opencode plugin is installed, but Beacon hook binary is missing at %s", out.BinaryPath)
			return out
		}
		data, err := os.ReadFile(out.PluginPath)
		if err != nil || !strings.Contains(string(data), out.BinaryPath) || strings.Contains(string(data), "__BEACON_") {
			out.Installed = false
			out.Message = fmt.Sprintf("opencode plugin at %s does not reference the active Beacon hook binary", out.PluginPath)
		}
	}
	return out
}

func opencodeEmbeddedPluginSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "opencode", "beacon.ts"))
}

func opencodeRootPluginSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "opencode-beacon", "src", "beacon.ts"))
}

func installOpenCodePlugin(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), opencodeManagedPluginMarker) {
			return fmt.Errorf("refusing to overwrite unmanaged opencode plugin at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	plugin, err := renderOpenCodePlugin(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(plugin), 0644)
}

func removeOpenCodePlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), opencodeManagedPluginMarker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isOpenCodeInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	return strings.Contains(text, opencodeManagedPluginMarker)
}

func opencodePluginPath(level Level) (string, error) {
	dir, err := opencodePluginDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, opencodePluginFileName), nil
}

func opencodePluginDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "opencode", "plugins"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".opencode", "plugins"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

func renderOpenCodePlugin(binaryPath, logPath, configPath string) (string, error) {
	return renderOpenCodePluginTemplate(opencodePluginTemplate, binaryPath, logPath, configPath)
}

func renderOpenCodePluginTemplate(template, binaryPath, logPath, configPath string) (string, error) {
	commandPrefix := endpointCommandPrefix("opencode", binaryPath, logPath, configPath)
	source := strings.ReplaceAll(template, "__BEACON_MANAGED_MARKER__", opencodeManagedPluginMarker)
	source = strings.ReplaceAll(source, `"__BEACON_COMMAND__"`, fmt.Sprintf("%q", commandPrefix+" opencode-event"))
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("opencode plugin template contains unresolved Beacon placeholders")
	}
	return source, nil
}
