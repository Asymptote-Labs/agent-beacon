package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	clineplugin "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/cline"
)

const (
	clinePluginFileName      = "beacon.ts"
	clineManagedPluginMarker = "beacon-managed-cline-plugin:v1"
	clineArgvPlaceholder     = `["__BEACON_ARGV__"]`
)

var clinePluginTemplate = clineplugin.Template

type ClineOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type ClineStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	PluginPath string `json:"plugin_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

var clineRuntime = hookRuntime{
	displayName: "Cline",
	configPath:  clinePluginPath,
	install:     installClinePlugin,
	uninstall:   removeClinePlugin,
	isInstalled: isClineInstalledAt,
}

func InstallCline(opts ClineOptions) (ClineStatus, error) {
	status, err := installRuntimeHooks(clineRuntime, RuntimeOptions(opts))
	if err != nil {
		return ClineStatus{}, err
	}
	return clineStatusFromRuntime(status), nil
}

func UninstallCline(opts ClineOptions) (ClineStatus, error) {
	status, err := uninstallRuntimeHooks(clineRuntime, RuntimeOptions(opts))
	if err != nil {
		return ClineStatus{}, err
	}
	return clineStatusFromRuntime(status), nil
}

func ClineHookStatus(opts ClineOptions) ClineStatus {
	return clineStatusFromRuntime(runtimeHookStatus(clineRuntime, RuntimeOptions(opts)))
}

func IsClineInstalled(opts ClineOptions) bool {
	return isRuntimeInstalled(clineRuntime, RuntimeOptions(opts))
}

// clineStatusFromRuntime reports installed only when the plugin can actually reach the hook binary.
//
// The marker alone is not enough. A plugin file survives a Beacon uninstall, a partially applied
// update, or a home directory restored from backup onto a machine where the binary lives elsewhere,
// and in each of those cases Cline loads a plugin that spawns nothing. Reporting that as installed
// tells an operator telemetry is being collected when none is.
func clineStatusFromRuntime(status runtimeStatus) ClineStatus {
	out := ClineStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		PluginPath: status.ConfigPath,
		Message:    status.Message,
	}
	if !out.Installed || out.BinaryPath == "" {
		return out
	}
	if _, err := os.Stat(out.BinaryPath); err != nil {
		out.Installed = false
		out.Message = fmt.Sprintf("Cline plugin is installed, but Beacon hook binary is missing at %s", out.BinaryPath)
		return out
	}
	data, err := os.ReadFile(out.PluginPath)
	if err != nil || !strings.Contains(string(data), out.BinaryPath) || strings.Contains(string(data), "__BEACON_") {
		out.Installed = false
		out.Message = fmt.Sprintf("Cline plugin at %s does not reference the active Beacon hook binary", out.PluginPath)
	}
	return out
}

func clineEmbeddedPluginSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "cline", "beacon.ts"))
}

func clineRootPluginSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "cline-beacon", "src", "beacon.ts"))
}

func installClinePlugin(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), clineManagedPluginMarker) {
			return fmt.Errorf("refusing to overwrite unmanaged Cline plugin at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	plugin, err := renderClinePlugin(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(plugin), 0644)
}

func removeClinePlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), clineManagedPluginMarker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isClineInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), clineManagedPluginMarker)
}

// clinePluginPath is where Cline auto-discovers plugins.
//
// Cline reads ~/.cline/plugins for global plugins and .cline/plugins in a repository for
// project-scoped ones, and discovers a bare .ts file dropped into either. That is why Beacon writes
// a file rather than running `cline plugin install`: no CLI has to be present, and an install works
// the same for the VS Code and JetBrains hosts, which ship no `cline` binary at all.
func clinePluginPath(level Level) (string, error) {
	dir, err := clinePluginDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, clinePluginFileName), nil
}

func clinePluginDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cline", "plugins"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".cline", "plugins"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

func renderClinePlugin(binaryPath, logPath, configPath string) (string, error) {
	return renderClinePluginTemplate(clinePluginTemplate, binaryPath, logPath, configPath)
}

// renderClinePluginTemplate substitutes the hook invocation into the plugin source.
//
// The invocation goes in as a JSON array of argv, not as a command line, because the plugin spawns
// the binary directly. That removes the shell -- and with it the per-shell quoting problem that
// endpointCommandPrefix documents -- from a runtime whose hosts include VS Code on Windows. JSON
// encoding also means a path containing a quote or a backslash needs no special handling: Windows
// paths are exactly that case.
func renderClinePluginTemplate(template, binaryPath, logPath, configPath string) (string, error) {
	args := append(endpointCommandArgs("cline", binaryPath, logPath, configPath), "cline-event")
	argv, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	source := strings.ReplaceAll(template, "__BEACON_MANAGED_MARKER__", clineManagedPluginMarker)
	if !strings.Contains(source, clineArgvPlaceholder) {
		return "", fmt.Errorf("cline plugin template is missing the %s placeholder", clineArgvPlaceholder)
	}
	source = strings.ReplaceAll(source, clineArgvPlaceholder, string(argv))
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("cline plugin template contains unresolved Beacon placeholders")
	}
	return source, nil
}
