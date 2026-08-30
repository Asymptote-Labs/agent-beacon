package hooks

import (
	"fmt"
	"os"
	"path/filepath"
)

// Pi (pi.dev) has no hooks configuration file and no OpenTelemetry support, so neither of Beacon's
// two established integration shapes applies to it. Its only observation surface is the TypeScript
// extension API, which makes the integration plugin-shaped. The mechanics of writing, checking and
// removing that file are shared with Prime Agent and live in piextension.go; what is Pi's alone is
// below.
const (
	// PiManagedExtensionMarker identifies an extension file as one Beacon wrote.
	//
	// Exported because three packages need the same answer and must not disagree: `hooks` writes
	// the marker and refuses to overwrite a file without it, `harness` reads it to report telemetry
	// status, and `inventory` reads it to decide whether a file it found is Beacon-managed. The
	// existing opencode and grok markers are spelled out as literals in each of those places
	// instead; a marker that drifts between writer and reader turns install into a no-op that
	// reports success, so this one has a single definition.
	//
	// The version suffix is part of the contract with the extension source, not decoration. Bump it
	// when the extension's behavior changes in a way that makes an older installed copy wrong,
	// which is what lets a repair recognize a stale file rather than leave it in place.
	PiManagedExtensionMarker = "beacon-managed-pi-extension:v1"
)

// piRuntime is the Pi half of the shared extension install. Its platform value is both the
// `--platform` flag the hook binary is invoked with and the runtime name the extension reads to
// pick its subscription list.
var piFamilyPi = piFamilyRuntime{
	platform:    "pi",
	hookCommand: "pi-event",
	marker:      PiManagedExtensionMarker,
	displayName: "Pi",
}

type PiOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type PiStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	ExtensionPath string `json:"extension_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var piRuntime = hookRuntime{
	displayName: "Pi",
	configPath:  PiExtensionPath,
	install:     installPiExtension,
	uninstall:   removePiExtension,
	isInstalled: isPiInstalledAt,
}

func InstallPi(opts PiOptions) (PiStatus, error) {
	status, err := installRuntimeHooks(piRuntime, RuntimeOptions(opts))
	if err != nil {
		return PiStatus{}, err
	}
	return piStatusFromRuntime(status), nil
}

func UninstallPi(opts PiOptions) (PiStatus, error) {
	status, err := uninstallRuntimeHooks(piRuntime, RuntimeOptions(opts))
	if err != nil {
		return PiStatus{}, err
	}
	return piStatusFromRuntime(status), nil
}

func PiHookStatus(opts PiOptions) PiStatus {
	return piStatusFromRuntime(runtimeHookStatus(piRuntime, RuntimeOptions(opts)))
}

func IsPiInstalled(opts PiOptions) bool {
	return isRuntimeInstalled(piRuntime, RuntimeOptions(opts))
}

// piStatusFromRuntime reports installed only when the extension can actually reach the hook binary.
func piStatusFromRuntime(status runtimeStatus) PiStatus {
	out := PiStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
	if !out.Installed || out.BinaryPath == "" {
		return out
	}
	if healthy, message := piFamilyExtensionHealth(piFamilyPi, out.ExtensionPath, out.BinaryPath); !healthy {
		out.Installed = false
		out.Message = message
	}
	return out
}

func installPiExtension(path, binaryPath, logPath, configPath string) error {
	return installPiFamilyExtension(piFamilyExtensionTemplate, piFamilyPi, path, binaryPath, logPath, configPath)
}

func removePiExtension(path string) (bool, error) {
	return removePiFamilyExtension(PiManagedExtensionMarker, path)
}

func isPiInstalledAt(path string) bool {
	return isPiFamilyExtensionInstalledAt(PiManagedExtensionMarker, path)
}

func renderPiExtension(binaryPath, logPath, configPath string) (string, error) {
	return renderPiFamilyExtension(piFamilyExtensionTemplate, piFamilyPi, binaryPath, logPath, configPath)
}

// PiExtensionPath returns the extension file Beacon manages for a given install level.
//
// Pi loads extensions from two documented locations, and the distinction matters beyond the path:
// a project-level extension is subject to Pi's project-trust prompt, so a user-level install is the
// one that works without further interaction. Callers that only report status still need both
// spellings, because a file found at either location is a Beacon install.
func PiExtensionPath(level Level) (string, error) {
	dir, err := piExtensionDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, piFamilyExtensionFileName), nil
}

func piExtensionDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".pi", "agent", "extensions"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".pi", "extensions"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
