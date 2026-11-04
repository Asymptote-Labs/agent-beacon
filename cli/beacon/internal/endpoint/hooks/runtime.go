package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/embedded"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/hookcmd"
	"gopkg.in/yaml.v3"
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

// endpointCommandPrefix builds the command a runtime will execute for each hook.
//
// The three endpoint settings are passed as flags rather than as an inline `VAR=value cmd` prefix.
// That prefix is a POSIX shell construct: valid in sh, bash and zsh, and not valid in cmd.exe or
// PowerShell, so it cannot deliver anything on a Windows runtime that uses either. Flags carry the
// same values with no shell semantics involved beyond quoting the paths.
//
// What a runtime actually does with this string was measured rather than assumed, because it decides
// the quoting and there is no form that works everywhere. Claude Code on Windows runs hook commands
// through Git Bash -- $BASH_VERSION expands, %COMSPEC% does not -- so the string is parsed by a POSIX
// shell on all three platforms for that runtime. Two results from the same measurement shape the code
// below: a quoted executable path followed by flags runs correctly, and an *unquoted* Windows path
// does not run at all, because bash consumes the backslashes as escapes.
// endpointCommandArgs builds the same invocation as endpointCommandPrefix, as argv rather than as a
// command line.
//
// Exists for runtimes that spawn the hook binary themselves instead of handing a string to a shell.
// A Cline plugin runs inside the VS Code extension host, a JetBrains host, or the CLI, on any
// platform, and passes argv straight to the OS -- which is the one form that needs no quoting at
// all. The comment on endpointCommandPrefix explains why quoting is otherwise unavoidably
// per-shell.
//
// The two functions must agree on which flags a hook receives; TestEndpointCommandArgsMatchPrefix
// is what keeps them from drifting.
func endpointCommandArgs(platform, binaryPath, logPath, configPath string) []string {
	args := []string{binaryPath, "--platform", platform}
	if logPath != "" {
		args = append(args, "--log", logPath)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	return args
}

func endpointCommandPrefix(platform, binaryPath, logPath, configPath string) string {
	args := []string{hookCommandQuote(binaryPath), "--platform", platform}
	if logPath != "" {
		args = append(args, "--log", hookCommandQuote(logPath))
	}
	if configPath != "" {
		args = append(args, "--config", hookCommandQuote(configPath))
	}
	return strings.Join(args, " ")
}

// hookCommandQuote quotes one value for the shell that will parse this command.
//
// Single quotes, on every platform. In a POSIX shell they are fully literal, which is what a Windows
// path needs: inside double quotes bash still processes `$`, a backtick, and a doubled backslash, so
// a profile directory containing a `$` would be expanded away and a UNC path would lose a separator.
// Single quotes have no such cases, and the measurement that showed Claude Code uses bash on Windows
// is what makes them the right choice there too.
//
// The cost is stated rather than hidden: this is not valid quoting in cmd.exe, and in PowerShell a
// quoted string in command position is an expression rather than a command. A Windows runtime that
// invokes hooks through either would need a different form -- so if one turns up, this is the function
// that grows a per-runtime branch, and the detection side already accepts both spellings.
func hookCommandQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ConfigHasEndpointHook reports whether a runtime's JSON or YAML hooks config holds a command
// hookcmd.IsEndpointHook accepts for platform. Every string under a "command" key is checked,
// wherever the runtime nests it. The error is the document's parse error.
func ConfigHasEndpointHook(data []byte, platform string) (bool, error) {
	var doc any
	var err error
	if json.Valid(data) {
		err = json.Unmarshal(data, &doc)
	} else {
		err = yaml.Unmarshal(data, &doc)
	}
	if err != nil {
		return false, err
	}
	return hasEndpointCommand(doc, platform), nil
}

func hasEndpointCommand(node any, platform string) bool {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			if command, ok := value.(string); ok && key == "command" && hookcmd.IsEndpointHook(command, platform) {
				return true
			}
			if hasEndpointCommand(value, platform) {
				return true
			}
		}
	case []any:
		for _, value := range typed {
			if hasEndpointCommand(value, platform) {
				return true
			}
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
