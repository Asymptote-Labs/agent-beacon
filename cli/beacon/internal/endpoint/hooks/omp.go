package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	ompextension "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks/assets/omp"
)

// Oh My Pi (`omp`) is integrated the same shape as Pi, which it forked: no hooks configuration file
// to merge into and no OpenTelemetry export, so the TypeScript extension API is the only
// observation surface. Beacon writes one extension file that forwards runtime events to the
// `beacon-hooks` binary, and managedExtension handles it exactly as it handles Pi's.
//
// The extension file itself is a separate source from Pi's rather than the same file installed
// twice. Oh My Pi exposes operator approval decisions and an operator Python surface that Pi does
// not, so the subscription lists genuinely differ, and each file carries its own marker so an
// install of one is never mistaken for an install of the other.
const (
	ompExtensionFileName = "beacon.ts"

	// OmpManagedExtensionMarker identifies an extension file as one Beacon wrote for Oh My Pi.
	//
	// Distinct from PiManagedExtensionMarker on purpose. The two runtimes install to different
	// directories today, but a shared marker would mean a status or repair pass could not tell
	// which runtime's extension it had found if either ever moved -- and an uninstall keyed on the
	// wrong marker removes the wrong file. Exported for the same three-package reason Pi's is.
	//
	// The version suffix is part of the contract with the extension source. Bump it when the
	// extension's behavior changes in a way that makes an older installed copy wrong, which is what
	// lets a repair recognize a stale file rather than leave it in place.
	OmpManagedExtensionMarker = "beacon-managed-omp-extension:v1"

	// ompConfigDirName is the config directory Oh My Pi keeps under the home directory.
	ompConfigDirName = ".omp"

	// ompAgentDirEnv overrides the agent directory of the default profile. A named profile ignores
	// it, though Oh My Pi exports it (with the profile variables) on its own process when one is
	// active, so a child of a profiled session sees the two agree.
	ompAgentDirEnv = "PI_CODING_AGENT_DIR"

	// ompConfigDirEnv renames the config directory under the home directory. Oh My Pi applies it
	// only to that home-directory root, never to the project directory -- see ompExtensionDir.
	ompConfigDirEnv = "PI_CONFIG_DIR"

	// ompProfileEnv names the active profile. ompLegacyProfileEnv is read only when ompProfileEnv
	// is unset, so an explicitly empty OMP_PROFILE selects the default profile.
	ompProfileEnv       = "OMP_PROFILE"
	ompLegacyProfileEnv = "PI_PROFILE"
)

// The profile names Oh My Pi accepts (normalizeProfileName in its utils/dirs.ts). It refuses to
// start with any other name, so Beacon refuses to resolve one rather than write where nothing
// reads.
var (
	ompProfileNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	ompReservedProfileName = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\..*)?$`)
)

// ompProfileName normalizes a profile name as Oh My Pi does: blank and "default" are the default
// profile, returned as "".
func ompProfileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "default" {
		return "", nil
	}
	if name == "." || name == ".." || strings.HasSuffix(name, ".") ||
		!ompProfileNamePattern.MatchString(name) || ompReservedProfileName.MatchString(name) {
		return "", fmt.Errorf("Oh My Pi profile %q is not a valid profile name", raw)
	}
	return name, nil
}

// OmpAgentDirForHome resolves Oh My Pi's user agent directory for the active profile: the
// directory that holds its user mcp.json and its extensions directory.
//
// It follows the runtime's own order (getAgentDir in its utils/dirs.ts). The active profile is
// OMP_PROFILE, or PI_PROFILE when OMP_PROFILE is unset; a named profile lives at
// `~/<config>/profiles/<name>/agent` and ignores PI_CODING_AGENT_DIR. The default profile honors
// PI_CODING_AGENT_DIR, except a value that is PI_PROFILE's own profile directory -- what a child
// of a profiled session that switched back to the default inherits -- and otherwise lives at
// `~/<config>/agent`. `<config>` is PI_CONFIG_DIR, or `.omp`.
func OmpAgentDirForHome(home string) (string, error) {
	configDir := ompConfigDirName
	if named := os.Getenv(ompConfigDirEnv); named != "" {
		configDir = named
	}
	profileDir := func(name string) string {
		return filepath.Join(home, configDir, "profiles", name, "agent")
	}
	rawProfile, set := os.LookupEnv(ompProfileEnv)
	if !set {
		rawProfile = os.Getenv(ompLegacyProfileEnv)
	}
	profile, err := ompProfileName(rawProfile)
	if err != nil {
		return "", err
	}
	if profile == "" {
		if agentDir := os.Getenv(ompAgentDirEnv); agentDir != "" {
			legacy, err := ompProfileName(os.Getenv(ompLegacyProfileEnv))
			if err != nil || legacy == "" || home == "" || agentDir != profileDir(legacy) {
				return filepath.Abs(agentDir)
			}
		}
	}
	if home == "" {
		return "", fmt.Errorf("home directory is required to resolve the Oh My Pi agent directory")
	}
	if profile != "" {
		return profileDir(profile), nil
	}
	return filepath.Join(home, configDir, "agent"), nil
}

type OmpOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OmpStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	ExtensionPath string `json:"extension_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var ompExtension = managedExtension{
	platform:    "omp",
	displayName: "Oh My Pi",
	marker:      OmpManagedExtensionMarker,
	template:    ompextension.Template,
	configPath:  OmpExtensionPath,
}

var ompRuntime = ompExtension.runtime()

func InstallOmp(opts OmpOptions) (OmpStatus, error) {
	status, err := installRuntimeHooks(ompRuntime, RuntimeOptions(opts))
	if err != nil {
		return OmpStatus{}, err
	}
	return ompStatusFromRuntime(status), nil
}

func UninstallOmp(opts OmpOptions) (OmpStatus, error) {
	status, err := uninstallRuntimeHooks(ompRuntime, RuntimeOptions(opts))
	if err != nil {
		return OmpStatus{}, err
	}
	return ompStatusFromRuntime(status), nil
}

func OmpHookStatus(opts OmpOptions) OmpStatus {
	return ompStatusFromRuntime(runtimeHookStatus(ompRuntime, RuntimeOptions(opts)))
}

func IsOmpInstalled(opts OmpOptions) bool {
	return isRuntimeInstalled(ompRuntime, RuntimeOptions(opts))
}

// ompStatusFromRuntime reports installed only when the extension can actually reach the hook binary.
func ompStatusFromRuntime(status runtimeStatus) OmpStatus {
	status = ompExtension.reachableStatus(status)
	return OmpStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		ExtensionPath: status.ConfigPath,
		Message:       status.Message,
	}
}

func ompEmbeddedExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("assets", "omp", "beacon.ts"))
}

func ompRootExtensionSourcePath() string {
	return filepath.Clean(filepath.Join("..", "..", "..", "..", "..", "plugins", "omp-beacon", "src", "beacon.ts"))
}

// OmpExtensionPath returns the extension file Beacon manages for a given install level.
//
// Oh My Pi auto-discovers extension modules from two directories, and unlike Pi neither is behind a
// trust prompt: `isProjectTrusted()` in its extension API always returns true, because `.omp`
// project-local inputs are already loaded unconditionally. A user-level install is still the
// default, because it follows the operator rather than one checkout.
func OmpExtensionPath(level Level) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// Only the user level needs a home directory. Failing here for a project install would
		// refuse a path that does not depend on the value that could not be read.
		if level == LevelProject {
			return OmpExtensionPathForHome("", level)
		}
		return "", err
	}
	return OmpExtensionPathForHome(home, level)
}

// OmpExtensionPathForHome is OmpExtensionPath against a caller-supplied home directory.
//
// Exported for inventory, which scans a home directory it was handed rather than the process's own
// -- a scan of another user's tree, or of a fixture. Sharing the resolution rather than rebuilding
// the path there is what keeps inventory from reporting on a file the installer does not write:
// this path is not a fixed string, since a profile or a PI_CODING_AGENT_DIR override moves it.
func OmpExtensionPathForHome(home string, level Level) (string, error) {
	dir, err := ompExtensionDir(home, level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ompExtensionFileName), nil
}

// ompExtensionDir resolves the directory Oh My Pi actually scans for extension modules.
//
// The two levels resolve asymmetrically, and the asymmetry is Oh My Pi's rather than a
// simplification here. Its user root is the active profile's *agent* directory, which
// OmpAgentDirForHome resolves. Its project root is the plain `.omp` directory in the working
// directory, with no `agent` segment and no environment override at all: the runtime joins the
// literal there.
//
// Deriving either path from the other, or applying `PI_CONFIG_DIR` to both, would write the file
// where Oh My Pi does not look -- and the install would report success and collect nothing.
func ompExtensionDir(home string, level Level) (string, error) {
	switch level {
	case "", LevelUser:
		agentDir, err := OmpAgentDirForHome(home)
		if err != nil {
			return "", err
		}
		return filepath.Join(agentDir, "extensions"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ompConfigDirName, "extensions"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
