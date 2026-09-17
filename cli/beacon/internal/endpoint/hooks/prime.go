package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	primeextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/prime"
)

// Prime Agent (Prime Intellect) is integrated the same shape as Pi, which it forked: no hooks
// configuration file to merge into and no OpenTelemetry export, so the TypeScript extension API is
// the only observation surface. Beacon writes one extension file that forwards runtime events to
// the `beacon-hooks` binary, and managedExtension handles it exactly as it handles Pi's and Oh My
// Pi's.
//
// The extension file is a separate source from Pi's rather than the same file installed twice.
// Prime Agent carries its own debug variable, its own test-sender symbol and its own comments about
// which of its events exist, and -- more to the point -- its own marker, so an install of one is
// never mistaken for an install of another.
const (
	primeExtensionFileName = "beacon.ts"

	// PrimeManagedExtensionMarker identifies an extension file as one Beacon wrote for Prime Agent.
	//
	// Distinct from PiManagedExtensionMarker and OmpManagedExtensionMarker on purpose. The three
	// runtimes install to different directories today, but a shared marker would mean a status or
	// repair pass could not tell which runtime's extension it had found if any of them ever moved
	// -- and an uninstall keyed on the wrong marker removes the wrong file. Exported for the same
	// three-package reason Pi's is: `hooks` writes it, `harness` reads it to report telemetry
	// status, and `inventory` reads it to decide whether a file it found is Beacon-managed.
	//
	// The version suffix is part of the contract with the extension source. Bump it when the
	// extension's behavior changes in a way that makes an older installed copy wrong, which is what
	// lets a repair recognize a stale file rather than leave it in place.
	PrimeManagedExtensionMarker = "beacon-managed-prime-extension:v1"

	// primeConfigDirName is the config directory Prime Agent keeps, at both scopes.
	//
	// Two path segments rather than one, and that is the runtime's own shape rather than a
	// convenience here: its CONFIG_DIR_NAME is the literal ".prime/agent", and it joins that whole
	// string under the home directory *and* under the working directory. So its project scope has
	// the `agent` segment that Pi's and Oh My Pi's project scopes do not. Copying either of those
	// layouts would write the file where Prime Agent does not look -- and the install would report
	// success and collect nothing.
	primeConfigDirName = ".prime/agent"

	// primeAgentDirEnv replaces the user-level agent directory outright.
	//
	// The name is derived rather than fixed upstream: Prime Agent builds it from the `piConfig.name`
	// in its own package.json, which is "prime-agent", so the shipped build reads
	// PRIME_AGENT_CODING_AGENT_DIR. PI_CODING_AGENT_DIR is what the same code produces for upstream
	// Pi and for Oh My Pi, and is deliberately not honored here: reading another product's variable
	// would move this install whenever an operator had set it for that one.
	primeAgentDirEnv = "PRIME_AGENT_CODING_AGENT_DIR"
)

type PrimeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type PrimeStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	ExtensionPath string `json:"extension_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var primeExtension = managedExtension{
	platform:    "prime",
	displayName: "Prime Agent",
	marker:      PrimeManagedExtensionMarker,
	template:    primeextension.Template,
	configPath:  PrimeExtensionPath,
}

var primeRuntime = primeExtension.runtime()

func InstallPrime(opts PrimeOptions) (PrimeStatus, error) {
	status, err := installRuntimeHooks(primeRuntime, RuntimeOptions(opts))
	if err != nil {
		return PrimeStatus{}, err
	}
	return primeStatusFromRuntime(status), nil
}

func UninstallPrime(opts PrimeOptions) (PrimeStatus, error) {
	status, err := uninstallRuntimeHooks(primeRuntime, RuntimeOptions(opts))
	if err != nil {
		return PrimeStatus{}, err
	}
	return primeStatusFromRuntime(status), nil
}

func PrimeHookStatus(opts PrimeOptions) PrimeStatus {
	return primeStatusFromRuntime(runtimeHookStatus(primeRuntime, RuntimeOptions(opts)))
}

func IsPrimeInstalled(opts PrimeOptions) bool {
	return isRuntimeInstalled(primeRuntime, RuntimeOptions(opts))
}

// primeStatusFromRuntime reports installed only when the extension can actually reach the binary.
func primeStatusFromRuntime(status runtimeStatus) PrimeStatus {
	status = primeExtension.reachableStatus(status)
	return PrimeStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
}

func primeEmbeddedExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "prime", "beacon.ts"))
}

func primeRootExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "prime-beacon", "src", "beacon.ts"))
}

// PrimeExtensionPath returns the extension file Beacon manages for a given install level.
//
// Prime Agent auto-discovers extension modules from both scopes with no trust prompt on either. A
// user-level install is still the default, because it follows the operator rather than one checkout.
func PrimeExtensionPath(level Level) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// Only the user level needs a home directory. Failing here for a project install would
		// refuse a path that does not depend on the value that could not be read.
		if level == LevelProject {
			return PrimeExtensionPathForHome("", level)
		}
		return "", err
	}
	return PrimeExtensionPathForHome(home, level)
}

// PrimeExtensionPathForHome is PrimeExtensionPath against a caller-supplied home directory.
//
// Exported for inventory, which scans a home directory it was handed rather than the process's own
// -- a scan of another user's tree, or of a fixture. Sharing the resolution rather than rebuilding
// the path there is what keeps inventory from reporting on a file the installer does not write:
// this path is not a fixed string, since PRIME_AGENT_CODING_AGENT_DIR moves it.
func PrimeExtensionPathForHome(home string, level Level) (string, error) {
	dir, err := primeExtensionDir(home, level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, primeExtensionFileName), nil
}

// primeExtensionDir resolves the directory Prime Agent actually scans for extension modules.
//
// The env override applies only to the user root, matching the runtime: it replaces getAgentDir(),
// and the project root is joined from the working directory with no variable consulted at all.
func primeExtensionDir(home string, level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if agentDir := os.Getenv(primeAgentDirEnv); agentDir != "" {
			expanded, err := primeExpandTilde(agentDir, home)
			if err != nil {
				return "", err
			}
			return filepath.Join(expanded, "extensions"), nil
		}
		if home == "" {
			return "", fmt.Errorf("home directory is required to resolve the Prime Agent extension path")
		}
		return filepath.Join(home, filepath.FromSlash(primeConfigDirName), "extensions"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, filepath.FromSlash(primeConfigDirName), "extensions"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

// primeExpandTilde resolves a leading `~` in the agent-directory override.
//
// Prime Agent expands it itself before using the value, so a Beacon install that did not would
// create a literal `~` directory beside the working directory and report success while the runtime
// kept reading the real home. Only a bare `~` or a `~/`-rooted path is expanded, which is the
// runtime's own rule -- `~user/...` is left alone by both.
func primeExpandTilde(path, home string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	if home == "" {
		return "", fmt.Errorf("home directory is required to resolve %s=%q", primeAgentDirEnv, path)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(path[2:])), nil
}
