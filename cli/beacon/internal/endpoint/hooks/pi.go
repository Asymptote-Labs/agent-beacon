package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	piextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/pi"
)

// Pi (pi.dev) has no hooks configuration file and no OpenTelemetry support, so neither of Beacon's
// two established integration shapes applies to it. Its only observation surface is the TypeScript
// extension API, which makes the integration plugin-shaped: Beacon writes one extension file that
// forwards runtime events to the `beacon-hooks` binary. That is the same shape as the opencode
// plugin, and the pieces below are the facts about that file which more than one package needs.
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

	// piArgvPlaceholder is the literal the extension source carries where the hook invocation goes.
	// Spelled out as a constant so the renderer can verify it was present before substituting: a
	// template edit that renamed it would otherwise install a file that spawns nothing and reports
	// success.
	piArgvPlaceholder = `["__BEACON_ARGV__"]`
)

var piExtensionTemplate = piextension.Template

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
//
// The marker alone is not enough, for the same reasons it is not enough for Cline: an extension file
// survives a Beacon uninstall, a partially applied update, or a home directory restored onto a
// machine where the binary lives elsewhere. In each case Pi loads an extension that spawns nothing,
// and reporting that as installed tells an operator telemetry is being collected when none is.
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
	if _, err := os.Stat(out.BinaryPath); err != nil {
		out.Installed = false
		out.Message = fmt.Sprintf("Pi extension is installed, but Beacon hook binary is missing at %s", out.BinaryPath)
		return out
	}
	data, err := os.ReadFile(out.ExtensionPath)
	if err != nil || !piExtensionReferencesBinary(string(data), out.BinaryPath) || strings.Contains(string(data), "__BEACON_") {
		out.Installed = false
		out.Message = fmt.Sprintf("Pi extension at %s does not reference the active Beacon hook binary", out.ExtensionPath)
	}
	return out
}

// piExtensionReferencesBinary reports whether a rendered extension spawns the given hook binary.
//
// The path is compared in the form the file actually holds it. The installer writes argv through
// json.Marshal, which escapes a backslash as two, so searching for the raw path finds nothing on
// Windows. Marshalling the path and stripping the surrounding quotes produces exactly the substring
// the file contains, on every platform, rather than a second escaping rule maintained by hand --
// the same fix clinePluginReferencesBinary documents from having gotten it wrong first.
func piExtensionReferencesBinary(source, binaryPath string) bool {
	if binaryPath == "" {
		return false
	}
	encoded, err := json.Marshal(binaryPath)
	if err != nil || len(encoded) < 2 {
		return false
	}
	return strings.Contains(source, string(encoded[1:len(encoded)-1]))
}

func installPiExtension(path, binaryPath, logPath, configPath string) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), PiManagedExtensionMarker) {
			return fmt.Errorf("refusing to overwrite unmanaged Pi extension at %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	extension, err := renderPiExtension(binaryPath, logPath, configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(extension), 0644)
}

func removePiExtension(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.Contains(string(data), PiManagedExtensionMarker) {
		return false, nil
	}
	return true, os.Remove(path)
}

func isPiInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), PiManagedExtensionMarker)
}

func renderPiExtension(binaryPath, logPath, configPath string) (string, error) {
	return renderPiExtensionTemplate(piExtensionTemplate, binaryPath, logPath, configPath)
}

// renderPiExtensionTemplate substitutes the hook invocation into the extension source.
//
// The invocation goes in as a JSON array of argv, not as a command line, because the extension
// spawns the binary directly. That removes the shell -- and with it the per-shell quoting problem
// endpointCommandPrefix documents -- from a runtime that ships as a Bun binary on Windows as
// readily as on macOS. JSON encoding also means a path containing a quote or a backslash needs no
// special handling, which is exactly what a Windows path is.
func renderPiExtensionTemplate(template, binaryPath, logPath, configPath string) (string, error) {
	args := append(endpointCommandArgs("pi", binaryPath, logPath, configPath), "pi-event")
	argv, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	source := strings.ReplaceAll(template, "__BEACON_MANAGED_MARKER__", PiManagedExtensionMarker)
	if !strings.Contains(source, piArgvPlaceholder) {
		return "", fmt.Errorf("pi extension template is missing the %s placeholder", piArgvPlaceholder)
	}
	source = strings.ReplaceAll(source, piArgvPlaceholder, string(argv))
	if strings.Contains(source, "__BEACON_") {
		return "", fmt.Errorf("pi extension template contains unresolved Beacon placeholders")
	}
	return source, nil
}

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
