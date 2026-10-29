package diagnostics

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
)

const repairSystemAction = "sudo beacon endpoint repair --system"

// logMarkedRestricted reads the log's restricted marker. A variable because only a marker root owns
// counts, and tests cannot be root.
var logMarkedRestricted = asymptoteobserve.RuntimeLogAccessRestricted

// checkLogPermissionsFor asks the question that fits the log's access mode. A shared log must be
// appendable by every hook; a restricted one must be appendable by none, which is the point of it.
func checkLogPermissionsFor(cfg endpointconfig.Config) Check {
	if cfg.RestrictedLogAccess() {
		return checkRestrictedLogPermissions(cfg.LogPath)
	}
	return checkLogPermissions(cfg.LogPath, cfg.UserMode)
}

// checkRestrictedLogPermissions reports a restricted log any account but root and the readers group
// can reach. Doctor usually runs with sudo; run without it, it cannot look inside the directory,
// which is itself the restricted mode working, and says so instead of guessing.
func checkRestrictedLogPermissions(path string) Check {
	dir := filepath.Dir(path)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail, Severity: SeverityMedium,
			Message: "the log directory is missing: " + err.Error(), Evidence: "log_dir_missing", Action: repairSystemAction}
	}
	if mode := dirInfo.Mode(); mode.Perm()&0o007 != 0 || mode.Perm()&0o020 != 0 {
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail, Severity: SeverityHigh,
			Message:  fmt.Sprintf("the restricted log's directory is mode %o, which lets other accounts reach or change it", mode.Perm()),
			Evidence: "restricted_log_dir_too_open", Action: repairSystemAction}
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK, Severity: SeverityInfo,
			Message:  "this account cannot open the restricted log, as intended; run doctor with sudo to inspect it",
			Evidence: "restricted_log_not_readable_by_caller"}
	case err != nil:
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusWarn, Severity: SeverityLow,
			Message: "runtime log not created yet", Evidence: "runtime_log_missing"}
	}
	if mode := info.Mode().Perm(); mode&0o007 != 0 || mode&0o020 != 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusFail, Severity: SeverityHigh,
			Message:  fmt.Sprintf("the restricted log is mode %o, so accounts outside root and the %s group can read or change it", mode, loggroup.Name),
			Evidence: "restricted_log_too_open", Action: repairSystemAction}
	}
	return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK, Severity: SeverityInfo,
		Message: fmt.Sprintf("mode %o, restricted", info.Mode().Perm()), Evidence: fmt.Sprintf("mode_%o", info.Mode().Perm())}
}

// checkLogAccess reports the system log's access mode and whether everything the mode depends on
// is in place: in the restricted mode, the marker that keeps the root writers from widening the
// log again, and the collector's ingest socket, without which every hook spools its events instead
// of recording them.
func checkLogAccess(cfg endpointconfig.Config, ingest localingest.Deliverer) Check {
	path := cfg.LogPath
	const name = "runtime_log_access"
	markerReadable := true
	if _, err := os.Stat(filepath.Dir(path)); errors.Is(err, fs.ErrPermission) {
		markerReadable = false
	} else if _, err := os.Stat(asymptoteobserve.LogAccessMarkerPath(path)); errors.Is(err, fs.ErrPermission) {
		markerReadable = false
	}
	marked := logMarkedRestricted(path)

	if !cfg.RestrictedLogAccess() {
		if marked {
			return Check{Name: name, Target: path, Status: StatusWarn, Severity: SeverityMedium,
				Message:  "the log is marked restricted but the endpoint config says shared, so hooks cannot append to it",
				Evidence: "log_access_mismatch", Action: repairSystemAction + " --log-access shared"}
		}
		if pending, err := ingest.Pending(path); err == nil && pending > 0 {
			// Left from a time the log was restricted, or refused this account; the account's
			// next event appends them.
			return Check{Name: name, Target: localingest.DefaultSpoolDir(), Status: StatusWarn, Severity: SeverityLow,
				Message:  fmt.Sprintf("shared; %d events from this account are waiting in its spool and go to the log with its next event", pending),
				Evidence: "ingest_spool_pending"}
		}
		return Check{Name: name, Target: path, Status: StatusOK, Severity: SeverityInfo,
			Message:  "shared: hooks append directly, and any local account can modify or read the log; `--log-access restricted` narrows it",
			Evidence: "log_access_shared"}
	}

	if markerReadable && !marked {
		return Check{Name: name, Target: path, Status: StatusFail, Severity: SeverityHigh,
			Message:  "the endpoint config says restricted but the log is not marked, so the collector will make it world-writable again at its next write",
			Evidence: "log_access_marker_missing", Action: repairSystemAction}
	}
	switch err := ingest.Client.Available(); {
	case errors.Is(err, localingest.ErrNoSocket):
		return Check{Name: name, Target: ingestSocketPath(ingest), Status: StatusFail, Severity: SeverityHigh,
			Message:  "the collector is not serving the ingest socket, so hooks cannot record events and are spooling them; start the collector",
			Evidence: "ingest_socket_missing", Action: repairSystemAction}
	case err != nil:
		return Check{Name: name, Target: ingestSocketPath(ingest), Status: StatusFail, Severity: SeverityHigh,
			Message: "hooks will not send to the ingest socket: " + err.Error(), Evidence: "ingest_socket_untrusted", Action: repairSystemAction}
	}
	if pending, err := ingest.Pending(path); err == nil && pending > 0 {
		return Check{Name: name, Target: localingest.DefaultSpoolDir(), Status: StatusWarn, Severity: SeverityLow,
			Message:  fmt.Sprintf("restricted; %d events from this account are waiting in its spool and go to the collector with the next event", pending),
			Evidence: "ingest_spool_pending"}
	}
	message := fmt.Sprintf("restricted: only the collector writes the log; root and the %s group read it", loggroup.Name)
	if !markerReadable {
		message += " (run doctor with sudo to inspect the log itself)"
	}
	return Check{Name: name, Target: path, Status: StatusOK, Severity: SeverityInfo, Message: message, Evidence: "log_access_restricted"}
}

func ingestSocketPath(ingest localingest.Deliverer) string {
	if ingest.Client.SocketPath != "" {
		return ingest.Client.SocketPath
	}
	return localingest.DefaultSocketPath
}
