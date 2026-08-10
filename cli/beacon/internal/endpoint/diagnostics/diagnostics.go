package diagnostics

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
)

type Check struct {
	Name     string `json:"name"`
	Target   string `json:"target,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Action   string `json:"action,omitempty"`
}

const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"

	SeverityInfo   = "info"
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

func Run(cfg endpointconfig.Config) []Check {
	checks := []Check{
		checkFile("config", endpointconfig.ConfigPath(cfg.UserMode), true),
		checkFile("collector_config", cfg.Collector.ConfigPath, true),
		checkFile("runtime_log", cfg.LogPath, false),
		checkLogPermissions(cfg.LogPath, cfg.UserMode),
		checkCollectorHealth(cfg),
	}
	// The service definition check is named after whichever manager is actually in use, so
	// the output is interpretable on a mixed fleet and a Linux host is not told to look for a
	// launchd plist. Supervised mode has no unit file to check -- its pidfile is runtime
	// state, not configuration, so a missing one just means "not started".
	mgr := service.Manager{UserMode: cfg.UserMode}
	switch mgr.ResolvedKind() {
	case service.KindLaunchd:
		if path, err := mgr.UnitPath(); err == nil {
			checks = append(checks, checkFile("launchd_plist", path, true))
		}
	case service.KindSystemd:
		if path, err := mgr.UnitPath(); err == nil {
			checks = append(checks, checkFile("systemd_unit", path, true))
		}
		// A --user unit stops at logout unless linger is set, and that failure is invisible
		// until the user next logs out and telemetry quietly stops. Surfaced as a warning
		// rather than a failure: collecting until logout is degraded, not broken.
		if cfg.UserMode {
			checks = append(checks, lingerCheck())
		}
	}
	return checks
}

func lingerCheck() Check {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return Check{Name: "systemd_user_linger", Status: StatusWarn,
			Message:  "could not determine the current user, so logout persistence is unverified",
			Evidence: "linger_unknown"}
	}
	if service.LingerEnabled(u.Username) {
		return Check{Name: "systemd_user_linger", Status: StatusOK, Target: u.Username,
			Message: "user unit will survive logout"}
	}
	return Check{Name: "systemd_user_linger", Status: StatusWarn, Target: u.Username,
		Message:  "linger is not enabled, so the collector stops when this user logs out",
		Evidence: "linger_disabled",
		Action:   "sudo loginctl enable-linger " + u.Username}
}

func checkCollectorHealth(cfg endpointconfig.Config) Check {
	status := collector.CheckStatus(cfg)
	if status.HealthReady {
		return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", collector.HealthCheckPort), Status: StatusOK, Severity: SeverityInfo, Message: "collector health check is ready", Evidence: "health_check_ready"}
	}
	message := status.Message
	if message == "" {
		message = "collector health check is not ready"
	}
	return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", collector.HealthCheckPort), Status: StatusWarn, Severity: SeverityMedium, Message: message, Evidence: "health_check_unavailable"}
}

func HasFailures(checks []Check) bool {
	for _, check := range checks {
		if check.Status == StatusFail {
			return true
		}
	}
	return false
}

func checkFile(name, path string, required bool) Check {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return Check{Name: name, Target: path, Status: StatusWarn, Severity: SeverityLow, Message: fmt.Sprintf("%s does not exist yet", path), Evidence: "missing_optional_file"}
		}
		return Check{Name: name, Target: path, Status: StatusFail, Severity: SeverityMedium, Message: err.Error(), Evidence: "stat_failed"}
	}
	if info.IsDir() {
		return Check{Name: name, Target: path, Status: StatusFail, Severity: SeverityMedium, Message: path + " is a directory", Evidence: "target_is_directory"}
	}
	return Check{Name: name, Target: path, Status: StatusOK, Severity: SeverityInfo, Message: path, Evidence: "file_exists"}
}

func checkLogPermissions(path string, userMode bool) Check {
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusWarn, Severity: SeverityLow, Message: "runtime log not created yet", Evidence: "runtime_log_missing"}
	}
	if runtime.GOOS == "windows" {
		// The two scopes fail differently there, so they are asked different questions. Only a
		// system-mode install has a privileged writer and unprivileged hooks; a user-mode log lives
		// in that user's own profile and is written by processes running as them.
		if userMode {
			return checkLogWritableByThisUser(path)
		}
		return checkLogACL(path)
	}
	mode := info.Mode().Perm()
	if mode&0222 == 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusFail, Severity: SeverityHigh, Message: fmt.Sprintf("runtime log is not writable: %o", mode), Evidence: "not_writable"}
	}
	if mode&0044 == 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusWarn, Severity: SeverityLow, Message: fmt.Sprintf("runtime log may not be readable by Wazuh: %o", mode), Evidence: "not_group_or_world_readable"}
	}
	return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK, Severity: SeverityInfo, Message: fmt.Sprintf("mode %o", mode), Evidence: fmt.Sprintf("mode_%o", mode)}
}

// checkLogWritableByThisUser is the user-mode Windows form of the question.
//
// The INTERACTIVE grant that system mode installs is deliberately absent here: a user-mode log
// holds that person's prompt text, and widening its ACL to every interactive account would hand it
// to anyone else who logs on. Running the system-mode check anyway would report a high-severity
// failure on every correctly configured user-mode endpoint -- a doctor that cries wolf on the
// normal case is one nobody reads when it finally has something to say.
//
// A test write is the right instrument in this scope, and only in this scope. The trap it falls
// into in system mode -- doctor runs elevated, so it proves access for a user whose access was
// never in question -- does not apply when the hooks run as the same person doctor does.
func checkLogWritableByThisUser(path string) Check {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusFail,
			Severity: SeverityHigh,
			Message:  "the runtime log is not writable by this user: " + err.Error(),
			Evidence: "not_writable"}
	}
	_ = f.Close()
	return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK,
		Severity: SeverityInfo, Message: "the runtime log is writable by this user",
		Evidence: "writable_by_owner"}
}

// checkLogACL is the system-mode Windows form of the same question: can the people whose sessions
// this endpoint captures write to its log?
//
// The mode-bit check above cannot answer it there. Windows reports 0666 for any ordinary file
// regardless of its ACL, so `mode&0222 == 0` is never true and the check would report OK for
// exactly the configuration it exists to catch -- a %ProgramData% log that only administrators can
// write, with every hook write failing silently.
//
// Read from the ACL rather than by attempting a write, because doctor usually runs elevated: a
// test write would succeed for the very user whose access is not in question.
func checkLogACL(path string) Check {
	dir := filepath.Dir(path)
	ok, err := writer.SystemLogWritableByUsers(dir)
	switch {
	case err != nil:
		// Unknown is reported as unknown. Claiming either answer here would be worse than saying
		// the check could not run.
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusWarn,
			Severity: SeverityLow, Message: "could not read the log directory ACL: " + err.Error(),
			Evidence: "acl_unreadable"}
	case !ok:
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail,
			Severity: SeverityHigh,
			Message: "interactive users cannot write to the log directory, so agent hooks will " +
				"fail silently while the collector reports healthy",
			Evidence: "acl_missing_interactive_write"}
	default:
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusOK,
			Severity: SeverityInfo, Message: "interactive users may write to the log directory",
			Evidence: "acl_interactive_write"}
	}
}
