package lifecycle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

// Seams for tests, which cannot be root or change the account database.
var (
	ensureLogReadersGroup   = loggroup.Ensure
	addLogReaderMember      = loggroup.AddMember
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
//
// When there was no log before, the install created it (with its lock, and for a restricted log
// the marker), and restoring removes exactly the files it created rather than choosing a mode for
// them. The package install scripts treat a log found without a system config as one kept by
// uninstall --keep-logs and keep its mode, so a log left behind by a failed first install would
// make the retry an upgrade: an MSI retry would install shared, and every script would skip adding
// the person who installed to the readers group.
func logAccessRestorer(cfg endpointconfig.Config) func() {
	wasRestricted := logIsRestricted(cfg.LogPath)
	created := logFilesAbsent(cfg.LogPath)
	return func() {
		if len(created) > 0 && created[0] == cfg.LogPath {
			for _, path := range created {
				_ = os.Remove(path)
			}
			return
		}
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

// logFilesAbsent lists which of the log, its lock and the access marker do not exist yet, the log
// first. A path that cannot be checked for any reason but its absence is left out, so a rollback
// never removes a file it could not see before.
func logFilesAbsent(logPath string) []string {
	var absent []string
	for _, path := range []string{logPath, logPath + ".lock", asymptoteobserve.LogAccessMarkerPath(logPath)} {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			absent = append(absent, path)
		}
	}
	return absent
}

// addLogReaders puts each named account in the readers group of a restricted system log. It never
// removes anyone, and an account that cannot be added (unknown, or a tool that failed) is reported
// rather than failing an install that has otherwise succeeded: a fleet install must not stop over
// one misspelled name. A shared log has no readers group, so the names are reported as ignored.
func addLogReaders(cfg endpointconfig.Config, readers []string) (added, problems []string) {
	if cfg.UserMode || len(readers) == 0 {
		return nil, nil
	}
	if !cfg.RestrictedLogAccess() {
		return nil, []string{fmt.Sprintf("ignored log readers %s: the system log is shared, so every account can already read it", strings.Join(readers, ", "))}
	}
	seen := map[string]bool{}
	for _, account := range readers {
		account = strings.TrimSpace(account)
		if account == "" || seen[account] {
			continue
		}
		seen[account] = true
		if err := addLogReaderMember(loggroup.Name, account); err != nil {
			problems = append(problems, fmt.Sprintf("could not add %s to the %s group: %v", account, loggroup.Name, err))
			continue
		}
		added = append(added, account)
	}
	return added, problems
}

// validateDashboard refuses a dashboard switch the endpoint cannot have. The switch belongs to
// the system config, which every account's dashboard reads; a user config's value would switch
// nothing off, so a user-mode install refuses to record one instead of appearing to.
func validateDashboard(cfg endpointconfig.Config) error {
	mode, err := endpointconfig.ParseDashboardMode(cfg.Dashboard)
	if err != nil {
		return err
	}
	if mode != "" && cfg.UserMode {
		return fmt.Errorf("--dashboard %s applies to a system install (--system); it is the administrator's switch for every account", mode)
	}
	return nil
}
