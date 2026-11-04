package devinsession

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
)

// DefaultDBPath is where Devin CLI keeps its session history on this platform.
//
// Devin CLI has no data-dir environment variable: on Windows it writes
// %APPDATA%\devin\cli\sessions.db, and everywhere else it follows the XDG base-directory
// convention -- $XDG_DATA_HOME/devin/cli/sessions.db when that file exists, otherwise
// ~/.local/share/devin/cli/sessions.db. The XDG candidate is only taken when it is already a
// file, so a machine with XDG_DATA_HOME set for other reasons does not send the collector
// to an empty directory while a real store sits under ~/.local/share.
func DefaultDBPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		base := strings.TrimSpace(os.Getenv("APPDATA"))
		if base == "" && home != "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		if base == "" {
			base = os.TempDir()
		}
		return filepath.Join(base, "devin", "cli", "sessions.db")
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		candidate := filepath.Join(xdg, "devin", "cli", "sessions.db")
		// Read through sessionfs like every other store path, so a system backfill confined
		// to the console user's home sees the same answer it would later get opening the file.
		if _, err := sessionfs.Stat(candidate); err == nil {
			return candidate
		}
	}
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "share", "devin", "cli", "sessions.db")
}

// DefaultStatePath is the per-user cursor file; same fallback as the other session collectors.
func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "devin.json")
}
