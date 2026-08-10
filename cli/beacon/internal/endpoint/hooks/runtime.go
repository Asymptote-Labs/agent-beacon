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

// isEndpointHookCommand decides whether a command already in a runtime's config is one Beacon wrote.
//
// This is the detection surface for status, repair and uninstall across every runtime, so both
// directions are costly. A false negative makes repair add a second hook beside the first and leaves
// uninstall's behind; a false positive rewrites or deletes a hook somebody else installed.
func isEndpointHookCommand(command, platform string) bool {
	hasPlatform := platform == "" || commandHasPlatform(command, platform)
	namesPlatform := commandNamesPlatform(command)
	hasBeaconBinary := strings.Contains(command, embedded.BinaryStem)
	hasLegacyBinary := strings.Contains(command, "asym-hooks")

	if commandCarriesEndpointSettings(command) && hasBeaconBinary {
		return hasPlatform || !namesPlatform
	}
	if hasBeaconBinary && !namesPlatform {
		return true
	}
	return hasLegacyBinary && hasPlatform
}

// commandCarriesEndpointSettings reports whether a command was written by an endpoint install, as
// opposed to being some other invocation of the same binary.
//
// Two spellings, because the values moved from the environment to flags. An inline
// `BEACON_ENDPOINT_MODE=1 ...` prefix is a POSIX shell construct that neither Windows shell accepts,
// so a Windows hook carries `--log`/`--config`/`--cli` instead. Both must be recognized: already
// installed POSIX hooks use the prefix, and until a repair rewrites them one machine has both.
//
// Recognizing only the prefix is what made this worth extracting. A flags-form command matched
// nothing -- it has no prefix, and it does name a platform, so it fell past both branches above and
// reported false. Every runtime's repair would have added a duplicate hook next to the one Beacon
// had just written, and uninstall would have left it in place.
func commandCarriesEndpointSettings(command string) bool {
	for _, field := range commandFields(command) {
		if field == "BEACON_ENDPOINT_MODE=1" {
			return true
		}
		for _, name := range []string{"--log", "--config", "--cli"} {
			if field == name || strings.HasPrefix(field, name+"=") {
				return true
			}
		}
	}
	return false
}

// commandNamesPlatform reports whether a command specifies a platform at all.
//
// A command that names none is treated as an any-platform install, which is how hooks written before
// the flag existed are still recognized. Asked as a token rather than by searching for the substring
// "--platform " with a trailing space: that spelling missed `--platform=cursor`, so such a hook was
// treated as platform-less and matched when asked about *any* runtime -- the false-positive direction,
// where repair rewrites somebody else's hook.
func commandNamesPlatform(command string) bool {
	for _, field := range commandFields(command) {
		if field == "--platform" || strings.HasPrefix(field, "--platform=") {
			return true
		}
	}
	return false
}

func commandHasPlatform(command, platform string) bool {
	fields := commandFields(command)
	for i, field := range fields {
		if field == "--platform" {
			return i+1 < len(fields) && fields[i+1] == platform
		}
		if value, found := strings.CutPrefix(field, "--platform="); found {
			return value == platform
		}
	}
	return false
}

// commandFields splits a hook command into argv-like tokens, keeping quoted runs together.
//
// strings.Fields is not enough once paths are Windows paths. The default install location is under
// %ProgramFiles% or a user profile, both of which routinely contain a space, so a quoted path splits
// into fragments and any token comparison after it is being made against half a path. Quoting is
// stripped rather than preserved because callers compare against bare values like "cursor".
//
// Both quote characters, because the command may have been written for either shell -- POSIX hooks
// are single-quoted and Windows ones double-quoted -- and this function only has to recover tokens,
// not to reproduce a particular shell's semantics. It deliberately does not handle backslash escapes:
// on Windows a backslash is a path separator, and treating it as an escape would corrupt every path
// this parses.
func commandFields(command string) []string {
	var (
		fields  []string
		current strings.Builder
		quote   rune
		started bool
	)
	flush := func() {
		if started {
			fields = append(fields, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				break
			}
			current.WriteRune(r)
		case r == '"' || r == '\'':
			// Marks the token as started even if the quotes turn out to be empty, so `--platform ""`
			// yields an empty value rather than dropping the argument and shifting every token after
			// it left by one.
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	flush()
	return fields
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
