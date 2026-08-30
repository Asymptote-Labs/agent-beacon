package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	piextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/pi"
)

// Pi (pi.dev) has no hooks configuration file and no OpenTelemetry support, so neither of Beacon's
// two established integration shapes applies to it. Its only observation surface is the TypeScript
// extension API, which makes the integration plugin-shaped: Beacon writes one extension file that
// forwards runtime events to the `beacon-hooks` binary. That is the same shape as the opencode
// plugin, and the handling of that file lives in managedExtension, shared with Oh My Pi and with
// Prime Agent, which renders from this same source.
const (
	piExtensionFileName = "beacon.ts"

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

var piExtension = managedExtension{
	platform:    "pi",
	displayName: "Pi",
	marker:      PiManagedExtensionMarker,
	template:    piextension.Template,
	// Shared with Prime Agent, which renders the same source with its own runtime name.
	sharedTemplate: true,
	configPath:     PiExtensionPath,
}

var piRuntime = piExtension.runtime()

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
	status = piExtension.reachableStatus(status)
	return PiStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
}

// The two copies of the extension source a test compares. The embedded one is what ships in the
// binary; the root one is what a contributor edits and `bun run sync` copies over. Prime Agent
// renders from this same file, so one drift check covers both distributions.
func piEmbeddedExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "pi", "beacon.ts"))
}

func piRootExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "pi-beacon", "src", "beacon.ts"))
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
	return filepath.Join(dir, piExtensionFileName), nil
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
