package asymptoteobserve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Log access modes for a machine-wide (system-mode) runtime log.
//
// Shared is how the system log has always worked: hooks running as each logged-in user append to it
// directly, so it is writable -- and readable -- by everyone (SharedRuntimeLogFileMode). Restricted
// is opt-in: the root collector is the only writer, hooks hand it their events over the local
// ingest socket (package localingest), and the log is readable only by root and the
// RuntimeLogReadersGroup. A per-user log in the user's home is private in either case and is not
// affected.
const (
	LogAccessShared     = "shared"
	LogAccessRestricted = "restricted"
)

const (
	// RestrictedRuntimeLogFileMode is a restricted system log's mode: root writes, the readers
	// group reads, nobody else has access.
	RestrictedRuntimeLogFileMode os.FileMode = 0o640
	// RestrictedRuntimeLogLockMode is the mode of a restricted log's lock: only root takes it.
	RestrictedRuntimeLogLockMode os.FileMode = 0o600
	// RestrictedRuntimeLogDirMode is its directory's: setgid, so every file the collector creates
	// there -- the log after a rotation, its lock -- takes the readers group, as it does on macOS
	// without the bit.
	RestrictedRuntimeLogDirMode os.FileMode = 0o750 | os.ModeSetgid

	// RuntimeLogReadersGroup is the group that may read a restricted system log: log shippers and
	// people who should see every user's sessions.
	RuntimeLogReadersGroup = "beacon-log"

	// LogAccessMarkerName is the file, beside the log, that records the restricted mode. It is the
	// mode's one source of truth for the root writers -- the collector, the ingest server and the
	// CLI running as root -- which otherwise know only the log's path. It counts only when root
	// owns it and nobody else can write it, so no other account can switch a log's mode.
	LogAccessMarkerName = ".beacon-log-access"

	maxLogAccessMarkerBytes = 64
)

// logAccessMarkerOwner is the uid that must own the marker; tests set their own.
var logAccessMarkerOwner = 0

// LogAccessMarkerPath is the marker for the runtime log at logPath.
func LogAccessMarkerPath(logPath string) string {
	return filepath.Join(filepath.Dir(logPath), LogAccessMarkerName)
}

// RuntimeLogAccessRestricted reports whether the system log at path is in the restricted mode. A
// private per-user log never is. A process that cannot read the log's directory -- an ordinary user
// once the directory is restricted -- sees false, and must not be writing there anyway.
func RuntimeLogAccessRestricted(path string) bool {
	if strings.TrimSpace(path) == "" || RuntimeLogIsPrivate(path) {
		return false
	}
	return readLogAccessMarker(LogAccessMarkerPath(path)) == LogAccessRestricted
}

// SetRuntimeLogAccess records the access mode for the system log at logPath: restricted writes the
// marker, shared removes it. It changes only the marker; the caller applies the modes and
// ownership that go with it. The write goes through a temporary file and a rename, so the marker
// is never seen half-written.
func SetRuntimeLogAccess(logPath, mode string) error {
	marker := LogAccessMarkerPath(logPath)
	switch mode {
	case LogAccessShared:
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", marker, err)
		}
		return nil
	case LogAccessRestricted:
	default:
		return fmt.Errorf("unknown log access mode %q (want %s or %s)", mode, LogAccessShared, LogAccessRestricted)
	}
	if RuntimeLogIsPrivate(logPath) {
		return fmt.Errorf("%s is a per-user log; the %s mode applies only to the system log", logPath, LogAccessRestricted)
	}
	dir := filepath.Dir(logPath)
	tmp, err := os.CreateTemp(dir, LogAccessMarkerName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(LogAccessRestricted + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, marker)
}

// ParseLogAccess normalizes a configured log access mode. Empty is shared.
func ParseLogAccess(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", LogAccessShared:
		return LogAccessShared, nil
	case LogAccessRestricted:
		return LogAccessRestricted, nil
	default:
		return "", fmt.Errorf("unknown log access mode %q (want %s or %s)", value, LogAccessShared, LogAccessRestricted)
	}
}
