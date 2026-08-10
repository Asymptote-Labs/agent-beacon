package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/embedded"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

type RuntimeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type runtimeStatus struct {
	Installed  bool
	BinaryPath string
	ConfigPath string
	Message    string
}

type hookRuntime struct {
	displayName string
	configPath  func(Level) (string, error)
	install     func(path, binaryPath, logPath, configPath string) error
	uninstall   func(path string) (bool, error)
	isInstalled func(path string) bool
}

func installRuntimeHooks(runtime hookRuntime, opts RuntimeOptions) (runtimeStatus, error) {
	if !embedded.HasEmbeddedBinary() {
		return runtimeStatus{}, fmt.Errorf("no hooks binary embedded")
	}
	if err := embedded.ValidateArchitecture(); err != nil {
		return runtimeStatus{}, fmt.Errorf("embedded hooks binary is not usable on this host: %w", err)
	}
	if opts.LogPath == "" {
		opts.LogPath = defaultLogPath(opts.UserMode)
	}
	configPath, err := runtime.configPath(opts.Level)
	if err != nil {
		return runtimeStatus{}, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return runtimeStatus{}, err
	}
	binaryPath, err := writeEndpointHookBinary(opts.UserMode)
	if err != nil {
		return runtimeStatus{}, err
	}
	hookConfigPath := endpointConfigPathForHook(opts.LogPath, opts.UserMode)
	if err := runtime.install(configPath, binaryPath, opts.LogPath, hookConfigPath); err != nil {
		return runtimeStatus{}, err
	}
	return runtimeStatus{
		Installed:  true,
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		Message:    fmt.Sprintf("%s endpoint hooks installed", runtime.displayName),
	}, nil
}

func uninstallRuntimeHooks(runtime hookRuntime, opts RuntimeOptions) (runtimeStatus, error) {
	configPath, err := runtime.configPath(opts.Level)
	if err != nil {
		return runtimeStatus{}, err
	}
	updated, err := runtime.uninstall(configPath)
	if err != nil {
		return runtimeStatus{}, err
	}
	status := runtimeStatus{
		ConfigPath: configPath,
		Message:    fmt.Sprintf("%s endpoint hooks were not present", runtime.displayName),
	}
	if updated {
		status.Message = fmt.Sprintf("%s endpoint hooks removed", runtime.displayName)
	}
	status.Installed = isRuntimeInstalled(runtime, opts)
	return status, nil
}

func runtimeHookStatus(runtime hookRuntime, opts RuntimeOptions) runtimeStatus {
	configPath, err := runtime.configPath(opts.Level)
	if err != nil {
		return runtimeStatus{Message: err.Error()}
	}
	status := runtimeStatus{ConfigPath: configPath}
	status.Installed = isRuntimeInstalled(runtime, opts)
	if status.Installed {
		status.Message = fmt.Sprintf("%s endpoint hooks are installed", runtime.displayName)
	} else {
		status.Message = fmt.Sprintf("%s endpoint hooks are not installed", runtime.displayName)
	}
	if path, err := endpointHookBinaryPath(opts.UserMode); err == nil {
		status.BinaryPath = path
	}
	return status
}

func isRuntimeInstalled(runtime hookRuntime, opts RuntimeOptions) bool {
	configPath, err := runtime.configPath(opts.Level)
	if err != nil {
		return false
	}
	return runtime.isInstalled(configPath)
}

func endpointCommandPrefix(platform, binaryPath, logPath, configPath string) string {
	cliEnv := ""
	if cliPath, err := os.Executable(); err == nil && cliPath != "" {
		cliEnv = " BEACON_ENDPOINT_CLI=" + shellQuote(cliPath)
	}
	return fmt.Sprintf("BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG=%s BEACON_ENDPOINT_CONFIG=%s%s %s --platform %s", shellQuote(logPath), shellQuote(configPath), cliEnv, shellQuote(binaryPath), platform)
}

func isEndpointHookCommand(command, platform string) bool {
	hasPlatform := platform == "" || commandHasPlatform(command, platform)
	hasBeaconBinary := strings.Contains(command, embedded.BinaryStem)
	hasLegacyBinary := strings.Contains(command, "asym-hooks")

	if strings.Contains(command, "BEACON_ENDPOINT_MODE=1") && hasBeaconBinary {
		return hasPlatform || !strings.Contains(command, "--platform ")
	}
	if hasBeaconBinary && !strings.Contains(command, "--platform ") {
		return true
	}
	return hasLegacyBinary && hasPlatform
}

func commandHasPlatform(command, platform string) bool {
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == "--platform" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"'`) == platform
		}
		if strings.HasPrefix(field, "--platform=") {
			return strings.Trim(strings.TrimPrefix(field, "--platform="), `"'`) == platform
		}
	}
	return false
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

func defaultLogPath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl")
		}
	}
	return endpointconfig.SystemLogPath()
}

// endpointConfigPathForHook picks which config a hook should read, from where its log lives.
//
// A log under a machine-wide location means the hook is feeding a system-mode endpoint, whatever
// scope the caller asked for, so it must read the system config. That was decided by matching the
// POSIX prefixes "/var/log/" and "/Library/" -- which named two of the three platforms' locations
// and no Windows one, so a Windows system install would have sent its hooks to the *user* config
// and pointed them at a log the collector never reads.
//
// Comparing against the resolved directories instead makes the question platform-independent, and
// keeps working if either location ever moves.
func endpointConfigPathForHook(logPath string, userMode bool) string {
	if underSystemLocation(logPath) {
		return endpointconfig.ConfigPath(false)
	}
	return endpointconfig.ConfigPath(userMode)
}

// underSystemLocation reports whether a path sits beneath a machine-wide Beacon directory.
//
// Case-insensitive on Windows, where paths are, and separator-normalized so a value built with
// either separator compares the same. A false negative here is the dangerous direction: it routes
// a system-mode hook to the user config, which fails silently rather than loudly.
func underSystemLocation(path string) bool {
	norm := func(p string) string {
		p = filepath.ToSlash(filepath.Clean(p))
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return strings.TrimSuffix(p, "/") + "/"
	}
	target := norm(path)
	for _, root := range []string{endpointconfig.SystemLogDir(), endpointconfig.SystemBaseDir()} {
		if root == "" {
			continue
		}
		if strings.HasPrefix(target, norm(root)) {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
