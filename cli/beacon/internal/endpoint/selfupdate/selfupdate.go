// Package selfupdate implements Beacon's endpoint update checks. Phase 1 is
// check-only: it discovers newer signed/notarized/stapled .pkg artifacts from a
// release manifest and records local system telemetry without downloading or
// applying package updates.
package selfupdate

import (
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"os"
	"path/filepath"
	"strings"
)

// SystemInstallDir is where a native package installs Beacon. Shared by the macOS .pkg and the
// Linux .deb/.rpm, so classifyInstall recognises both as a system install.
const (
	SystemInstallDir = "/opt/beacon"
	SystemBinDir     = "/opt/beacon/bin"
)

// SystemSupportDir holds endpoint config and self-update state for the system install.
//
// A function rather than a const, because the location differs by platform:
// /Library/Application Support/... on macOS, /etc/beacon/endpoint on Linux. It was previously
// hardcoded to the macOS path, so on Linux the update state directory and the managed-config
// path both resolved somewhere that does not exist.
func SystemSupportDir() string { return endpointconfig.SystemBaseDir() }

// InstallKind classifies how the running beacon binary was installed, which
// determines whether seamless self-update is possible.
type InstallKind string

const (
	// InstallSystemPkg is the MDM/.pkg install under /opt/beacon. This is the
	// only kind the seamless updater applies to.
	InstallSystemPkg InstallKind = "system_pkg"
	// InstallHomebrew is a `brew install beacon` binary; updates go through brew.
	InstallHomebrew InstallKind = "homebrew"
	// InstallOther is any other location (manual download, user dir, go run).
	InstallOther InstallKind = "other"
)

// Install describes the running beacon binary's provenance.
type Install struct {
	Kind       InstallKind
	BinaryPath string
}

// SupportsSeamlessUpdate reports whether the .pkg-based updater applies here.
func (i Install) SupportsSeamlessUpdate() bool {
	return i.Kind == InstallSystemPkg
}

// DetectInstall classifies the currently running beacon binary.
func DetectInstall() Install {
	exe, err := os.Executable()
	if err != nil {
		return Install{Kind: InstallOther}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return classifyInstall(exe)
}

// classifyInstall is the testable core of DetectInstall.
func classifyInstall(exe string) Install {
	clean := filepath.Clean(exe)
	switch {
	case clean == filepath.Join(SystemBinDir, "beacon") || strings.HasPrefix(clean, SystemInstallDir+string(filepath.Separator)):
		return Install{Kind: InstallSystemPkg, BinaryPath: clean}
	case isHomebrewPath(clean):
		return Install{Kind: InstallHomebrew, BinaryPath: clean}
	default:
		return Install{Kind: InstallOther, BinaryPath: clean}
	}
}

func isHomebrewPath(path string) bool {
	// Homebrew installs land under the Cellar (then symlinked into bin). We
	// resolve symlinks in DetectInstall, so the real path is in the Cellar, but
	// also match common prefixes defensively.
	if strings.Contains(path, string(filepath.Separator)+"Cellar"+string(filepath.Separator)) {
		return true
	}
	for _, prefix := range []string{"/opt/homebrew/", "/usr/local/Homebrew/", "/home/linuxbrew/.linuxbrew/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// StateDir is where the updater stores its lock, staged packages, and state
// for the system install.
func StateDir() string {
	return filepath.Join(SystemSupportDir(), "updates")
}

// SystemBeaconPath is the installed beacon binary the launchd updater invokes.
func SystemBeaconPath() string {
	return filepath.Join(SystemBinDir, "beacon")
}
