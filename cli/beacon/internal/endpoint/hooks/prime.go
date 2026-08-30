package hooks

import (
	"fmt"
	"os"
	"path/filepath"
)

// Prime Agent (Prime Intellect) is observed the same way Pi is, and for the same reasons: no hooks
// configuration file to merge into, no OpenTelemetry support to point at the local collector, and a
// TypeScript extension API that is its documented observation surface. The mechanics of writing and
// checking that file live in piextension.go; what is below is Prime Agent's alone.
const (
	// PrimeManagedExtensionMarker identifies an extension file as one Beacon wrote for Prime Agent.
	//
	// Exported for the same three readers PiManagedExtensionMarker is: `hooks` writes it and
	// refuses to overwrite a file without it, `harness` reads it to report telemetry status, and
	// `inventory` reads it to decide whether a file it found is Beacon-managed.
	//
	// Deliberately not Pi's marker, even though the rendered files differ only in what the
	// installer substituted. The two runtimes have separate extension directories and separate
	// install commands, and a shared marker would let a Prime Agent uninstall remove a file it did
	// not write -- and let either runtime's status report the other's install as its own.
	//
	// The version suffix is part of the contract with the extension source, not decoration. Bump it
	// when the extension's behavior changes in a way that makes an older installed copy wrong,
	// which is what lets a repair recognize a stale file rather than leave it in place.
	PrimeManagedExtensionMarker = "beacon-managed-prime-extension:v1"
)

// primeFamilyRuntime is the Prime Agent half of the shared extension install. Its platform value is
// both the `--platform` flag the hook binary is invoked with and the runtime name the extension
// reads to pick its subscription list.
var primeFamilyRuntime = piFamilyRuntime{
	platform:    "prime",
	hookCommand: "prime-event",
	marker:      PrimeManagedExtensionMarker,
	displayName: "Prime Agent",
}

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

var primeRuntime = hookRuntime{
	displayName: "Prime Agent",
	configPath:  PrimeExtensionPath,
	install:     installPrimeExtension,
	uninstall:   removePrimeExtension,
	isInstalled: isPrimeInstalledAt,
}

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

// primeStatusFromRuntime reports installed only when the extension can actually reach the hook
// binary.
func primeStatusFromRuntime(status runtimeStatus) PrimeStatus {
	out := PrimeStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
	if !out.Installed || out.BinaryPath == "" {
		return out
	}
	if healthy, message := piFamilyExtensionHealth(primeFamilyRuntime, out.ExtensionPath, out.BinaryPath); !healthy {
		out.Installed = false
		out.Message = message
	}
	return out
}

func installPrimeExtension(path, binaryPath, logPath, configPath string) error {
	return installPiFamilyExtension(piFamilyExtensionTemplate, primeFamilyRuntime, path, binaryPath, logPath, configPath)
}

func removePrimeExtension(path string) (bool, error) {
	return removePiFamilyExtension(PrimeManagedExtensionMarker, path)
}

func isPrimeInstalledAt(path string) bool {
	return isPiFamilyExtensionInstalledAt(PrimeManagedExtensionMarker, path)
}

func renderPrimeExtension(binaryPath, logPath, configPath string) (string, error) {
	return renderPiFamilyExtension(piFamilyExtensionTemplate, primeFamilyRuntime, binaryPath, logPath, configPath)
}

// PrimeExtensionPath returns the extension file Beacon manages for a given install level.
//
// Prime Agent loads extensions from two locations, and unlike Pi both of them carry the full config
// directory name: its loader joins the same `.prime/agent` root to the home directory and to the
// working directory. Deriving Prime Agent's project path from Pi's shape -- where the `agent`
// segment exists only under the home directory -- would write the file where the runtime does not
// look for it, so the install would report success and collect nothing.
func PrimeExtensionPath(level Level) (string, error) {
	dir, err := primeExtensionDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, piFamilyExtensionFileName), nil
}

// primeExtensionDir resolves the directory for a level.
//
// The PRIME_AGENT_CODING_AGENT_DIR override the runtime honors is deliberately not read here.
// Install, status, discovery and inventory all have to agree on one path, and they run in different
// processes -- `beacon endpoint hooks install` in a shell, `beacon endpoint status` from a service
// -- so a path that depended on the caller's environment would have install write one file and
// status report on another. A user who relocates the agent directory installs the extension there
// by hand; a Beacon that guessed would be confidently wrong.
func primeExtensionDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".prime", "agent", "extensions"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".prime", "agent", "extensions"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
