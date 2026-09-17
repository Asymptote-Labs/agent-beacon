package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	omoextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/omo"
)

// Senpi is the standalone edition of oh-my-openagent (OMO): an in-flight fork of pi-mono
// (code-yeongyu/senpi) that OMO brands and bundles its own extension into, distributed as the
// `omo` command. It is integrated the same shape as Pi and Prime Agent, which it shares an
// extension API with: no hooks configuration file to merge into and no OpenTelemetry export, so
// the TypeScript extension API is the only observation surface. Beacon writes one extension file
// that forwards runtime events to the `beacon-hooks` binary, and managedExtension handles it
// exactly as it handles Pi's, Oh My Pi's and Prime Agent's.
//
// The extension file is a separate source from the other three rather than the same file installed
// twice. It carries its own debug variable, its own test-sender symbol and its own comments about
// which of Senpi's events exist, and -- more to the point -- its own marker, so an install of one
// runtime is never mistaken for an install of another.
const (
	omoExtensionFileName = "beacon.ts"

	// OmoManagedExtensionMarker identifies an extension file as one Beacon wrote for Senpi.
	//
	// Distinct from PiManagedExtensionMarker, OmpManagedExtensionMarker and
	// PrimeManagedExtensionMarker on purpose. The four runtimes install to different directories
	// today, but a shared marker would mean a status or repair pass could not tell which runtime's
	// extension it had found if any of them ever moved -- and an uninstall keyed on the wrong
	// marker removes the wrong file. Exported for the same three-package reason Pi's is: `hooks`
	// writes it, `harness` reads it to report telemetry status, and `inventory` reads it to decide
	// whether a file it found is Beacon-managed.
	//
	// The version suffix is part of the contract with the extension source. Bump it when the
	// extension's behavior changes in a way that makes an older installed copy wrong, which is
	// what lets a repair recognize a stale file rather than leave it in place.
	OmoManagedExtensionMarker = "beacon-managed-omo-extension:v1"

	// omoConfigDirName is the config directory OMO's Senpi engine keeps, at both scopes.
	//
	// Two path segments rather than one, and that is the runtime's own shape rather than a
	// convenience here, exactly as it is for Prime Agent: this brand's CONFIG_DIR_NAME is ".omo",
	// its config layout is not flat, and its own directory resolver
	// (findNearestParentConfigDir(cwd, homeDir, ".omo", "agent")) joins the "agent" segment under
	// the working directory just as it does under the home directory. So its project scope has the
	// `agent` segment that Pi's and Oh My Pi's project scopes do not. Copying either of those
	// layouts would write the file where Senpi does not look -- and the install would report
	// success and collect nothing.
	//
	// Unlike Prime Agent, Beacon's installer does not replicate the runtime's own upward parent-
	// directory search: Senpi walks from the working directory toward home looking for an existing
	// `.omo/agent`, falling back to `~/.omo/agent` only when none is found. The project-level
	// install here always targets `<cwd>/.omo/agent`, matching the convention every other pi-family
	// installer in this package uses -- an install run from the same directory the operator later
	// runs `omo` from still lands exactly where the runtime looks.
	omoConfigDirName = ".omo/agent"

	// omoAgentDirEnv replaces the user-level agent directory outright.
	//
	// Senpi's own package.json brands this build's envPrefix as OMO, so the shipped `omo` binary
	// reads OMO_CODING_AGENT_DIR first. Its brand profile also keeps two legacy prefixes readable
	// -- SENPI (the upstream engine's own name) and PI (pi-mono's) -- checked in that order when
	// the OMO-prefixed variable is unset, so Beacon's install lands where the runtime actually
	// looks even for an operator who set one of those before OMO existed. PI_CODING_AGENT_DIR is
	// deliberately not read ahead of SENPI_CODING_AGENT_DIR: that is the runtime's own declared
	// precedence, most specific first.
	omoAgentDirEnv       = "OMO_CODING_AGENT_DIR"
	omoAgentDirLegacyEnv = "SENPI_CODING_AGENT_DIR"
	omoAgentDirPiEnv     = "PI_CODING_AGENT_DIR"
)

// omoAgentDirEnvNames lists the environment variables Senpi itself consults for its agent
// directory, most specific first, so omoAgentDirOverride can mirror the runtime's own precedence
// exactly rather than reading only the newest of the three.
var omoAgentDirEnvNames = []string{omoAgentDirEnv, omoAgentDirLegacyEnv, omoAgentDirPiEnv}

type OmoOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OmoStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	ExtensionPath string `json:"extension_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var omoExtension = managedExtension{
	platform:    "omo",
	displayName: "Senpi",
	marker:      OmoManagedExtensionMarker,
	template:    omoextension.Template,
	configPath:  OmoExtensionPath,
}

var omoRuntime = omoExtension.runtime()

func InstallOmo(opts OmoOptions) (OmoStatus, error) {
	status, err := installRuntimeHooks(omoRuntime, RuntimeOptions(opts))
	if err != nil {
		return OmoStatus{}, err
	}
	return omoStatusFromRuntime(status), nil
}

func UninstallOmo(opts OmoOptions) (OmoStatus, error) {
	status, err := uninstallRuntimeHooks(omoRuntime, RuntimeOptions(opts))
	if err != nil {
		return OmoStatus{}, err
	}
	return omoStatusFromRuntime(status), nil
}

func OmoHookStatus(opts OmoOptions) OmoStatus {
	return omoStatusFromRuntime(runtimeHookStatus(omoRuntime, RuntimeOptions(opts)))
}

func IsOmoInstalled(opts OmoOptions) bool {
	return isRuntimeInstalled(omoRuntime, RuntimeOptions(opts))
}

// omoStatusFromRuntime reports installed only when the extension can actually reach the binary.
func omoStatusFromRuntime(status runtimeStatus) OmoStatus {
	status = omoExtension.reachableStatus(status)
	return OmoStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
}

func omoEmbeddedExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "omo", "beacon.ts"))
}

func omoRootExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "omo-beacon", "src", "beacon.ts"))
}

// OmoExtensionPath returns the extension file Beacon manages for a given install level.
//
// Senpi auto-discovers extension modules from both scopes with no trust prompt on either. A
// user-level install is still the default, because it follows the operator rather than one
// checkout.
func OmoExtensionPath(level Level) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// Only the user level needs a home directory. Failing here for a project install would
		// refuse a path that does not depend on the value that could not be read.
		if level == LevelProject {
			return OmoExtensionPathForHome("", level)
		}
		return "", err
	}
	return OmoExtensionPathForHome(home, level)
}

// OmoExtensionPathForHome is OmoExtensionPath against a caller-supplied home directory.
//
// Exported for inventory, which scans a home directory it was handed rather than the process's own
// -- a scan of another user's tree, or of a fixture. Sharing the resolution rather than rebuilding
// the path there is what keeps inventory from reporting on a file the installer does not write:
// this path is not a fixed string, since OMO_CODING_AGENT_DIR (or its legacy fallbacks) moves it.
func OmoExtensionPathForHome(home string, level Level) (string, error) {
	dir, err := omoExtensionDir(home, level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, omoExtensionFileName), nil
}

// omoAgentDirOverride returns the first defined agent-directory override, checked in the same
// order Senpi's own brandEnvNames does. An explicitly empty value still counts as defined and
// short-circuits the fallback, matching the runtime's documented "first DEFINED value" semantics
// -- os.Getenv alone cannot distinguish "set to empty" from "unset", so LookupEnv is used here
// even though the other pi-family installers in this package do not need to make that distinction.
func omoAgentDirOverride() string {
	for _, name := range omoAgentDirEnvNames {
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
	}
	return ""
}

// omoExtensionDir resolves the directory Senpi actually scans for extension modules.
//
// The env override applies only to the user root, matching the runtime: it replaces
// getAgentDir(), and the project root is joined from the working directory with no variable
// consulted at all.
func omoExtensionDir(home string, level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if agentDir := omoAgentDirOverride(); agentDir != "" {
			expanded, err := omoExpandTilde(agentDir, home)
			if err != nil {
				return "", err
			}
			return filepath.Join(expanded, "extensions"), nil
		}
		if home == "" {
			return "", fmt.Errorf("home directory is required to resolve the Senpi extension path")
		}
		return filepath.Join(home, filepath.FromSlash(omoConfigDirName), "extensions"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, filepath.FromSlash(omoConfigDirName), "extensions"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

// omoExpandTilde resolves a leading `~` in the agent-directory override.
//
// Senpi expands it itself before using the value, so a Beacon install that did not would create a
// literal `~` directory beside the working directory and report success while the runtime kept
// reading the real home. Only a bare `~` or a `~/`-rooted path is expanded, which is the runtime's
// own rule -- `~user/...` is left alone by both.
func omoExpandTilde(path, home string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	if home == "" {
		return "", fmt.Errorf("home directory is required to resolve %q", path)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(path[2:])), nil
}
