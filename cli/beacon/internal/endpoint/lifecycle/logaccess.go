package lifecycle

import (
	"fmt"
	"runtime"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// Seams for tests, which cannot be root or change the account database.
var (
	ensureLogReadersGroup   = loggroup.Ensure
	logIsRestricted         = asymptoteobserve.RuntimeLogAccessRestricted
	removeStaleIngestSocket = localingest.RemoveStale
	restrictSystemLog       = writer.RestrictSystemLog
	shareSystemLog          = writer.ShareSystemLog
)

// validateLogAccess refuses a log access mode the endpoint cannot have, before install writes
// anything.
func validateLogAccess(cfg endpointconfig.Config) error {
	mode, err := asymptoteobserve.ParseLogAccess(cfg.LogAccess)
	if err != nil {
		return err
	}
	if mode != asymptoteobserve.LogAccessRestricted {
		return nil
	}
	if cfg.UserMode {
		return fmt.Errorf("--log-access %s applies to a system install; a user-mode log is already private to its user", mode)
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("--log-access %s is not available on Windows yet; the system log keeps its shared access", mode)
	}
	return nil
}

// logAccessOf is the mode an install reports: the system log's, or nothing in user mode.
func logAccessOf(cfg endpointconfig.Config) string {
	if cfg.UserMode {
		return ""
	}
	if cfg.RestrictedLogAccess() {
		return asymptoteobserve.LogAccessRestricted
	}
	return asymptoteobserve.LogAccessShared
}

// prepareSystemLog readies the system log directory for the configured access mode before anything
// writes there, and reports whether it created the readers group.
//
// Shared is what install has always done: a directory every hook can reach and a log every hook can
// append to. Switching back from restricted also removes the marker and widens the files again, so
// hooks can append the moment the collector stops serving the socket. Restricted creates the
// beacon-log group if it is missing, then narrows the directory and every log file to root and that
// group, after writing the marker that keeps the root writers from widening them again.
func prepareSystemLog(cfg endpointconfig.Config) (groupCreated bool, err error) {
	if !cfg.RestrictedLogAccess() {
		// A collector that was killed while restricted leaves its socket behind. Hooks fall back
		// to appending directly either way, but each would try the dead socket first.
		_ = removeStaleIngestSocket(localingest.DefaultSocketPath)
		return false, shareSystemLog(cfg.LogPath)
	}
	gid, created, err := ensureLogReadersGroup(loggroup.Name)
	if err != nil {
		return false, fmt.Errorf("the %s group: %w", loggroup.Name, err)
	}
	if err := restrictSystemLog(cfg.LogPath, gid); err != nil {
		return created, err
	}
	return created, nil
}

// logAccessRestorer records the system log's access mode before an install changes it and returns
// what puts it back. Restoring is best effort, like the rest of a rollback.
func logAccessRestorer(cfg endpointconfig.Config) func() {
	wasRestricted := logIsRestricted(cfg.LogPath)
	return func() {
		if logIsRestricted(cfg.LogPath) == wasRestricted {
			return
		}
		if wasRestricted {
			if gid, _, err := ensureLogReadersGroup(loggroup.Name); err == nil {
				_ = restrictSystemLog(cfg.LogPath, gid)
			}
			return
		}
		_ = shareSystemLog(cfg.LogPath)
	}
}
