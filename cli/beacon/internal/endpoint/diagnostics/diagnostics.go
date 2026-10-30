package diagnostics

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/collector"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/localingest"
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
		checkLogPermissionsFor(cfg),
		checkCollectorHealth(cfg),
	}
	if !cfg.UserMode {
		checks = append(checks, checkLogAccess(cfg, localingest.Deliverer{}))
		if runtime.GOOS == "windows" && endpointconfig.HasSecretDestinations(cfg) {
			checks = append(checks, checkSecretFilesPrivate(cfg, sysacl.InspectPrivateFile))
		}
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
			// A home directory on an external volume is the one layout where the plist being
			// present says nothing about whether launchd will load it (#639).
			if cfg.UserMode {
				checks = append(checks, launchAgentVolumeCheck(service.InspectLaunchAgentVolume(path)))
			}
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

// launchAgentVolumeCheck reports a LaunchAgents directory off the startup volume. launchd refuses to
// bootstrap plists from there ("Bootstrap failed: 5: Input/output error"), so Beacon bootstraps a
// staged copy on the startup volume instead. That works for the running session, but it is a
// layout worth naming: whether launchd's own login scan loads the canonical plist is not something
// Beacon can see, and a collector missing after a restart is fixed by a repair, which restages it.
func launchAgentVolumeCheck(vol service.LaunchAgentVolume) Check {
	if !vol.External {
		return Check{Name: "launch_agents_volume", Target: vol.PlistDir, Status: StatusOK, Severity: SeverityInfo,
			Message: "LaunchAgents is on the startup volume", Evidence: "launch_agents_startup_volume"}
	}
	if vol.StagingDir == "" {
		detail := "no private directory on the startup volume was usable"
		if vol.StagingError != "" {
			detail += ": " + vol.StagingError
		}
		return Check{Name: "launch_agents_volume", Target: vol.PlistDir, Status: StatusFail, Severity: SeverityHigh,
			Message:  vol.Reason + ", so launchd refuses to bootstrap Beacon's LaunchAgents from it, and " + detail,
			Evidence: "launch_agents_external_volume_unstaged",
			Action: "install with --no-start, copy the plists from " + vol.PlistDir + " into a private directory on the " +
				"startup volume, and run `launchctl bootstrap gui/$(id -u) <copied plist>` for each"}
	}
	return Check{Name: "launch_agents_volume", Target: vol.PlistDir, Status: StatusWarn, Severity: SeverityMedium,
		Message: vol.Reason + ", which launchd will not bootstrap LaunchAgents from; Beacon bootstraps copies staged in " +
			vol.StagingDir + " instead for this login session. Automatic loading after logout or restart is not guaranteed; " +
			"if the collector is not running, repair the install to restage the jobs",
		Evidence: "launch_agents_external_volume_staged",
		Action:   "beacon endpoint repair --user"}
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
		Action:   service.LingerRemediation(u.Username)}
}

func checkCollectorHealth(cfg endpointconfig.Config) Check {
	status := collector.CheckStatus(cfg)
	if status.HealthReady {
		return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", status.HealthPort), Status: StatusOK, Severity: SeverityInfo, Message: "collector health check is ready", Evidence: "health_check_ready"}
	}
	message := status.Message
	if message == "" {
		message = "collector health check is not ready"
	}
	return Check{Name: "collector_health", Target: fmt.Sprintf("127.0.0.1:%d", status.HealthPort), Status: StatusWarn, Severity: SeverityMedium, Message: message, Evidence: "health_check_unavailable"}
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
	// System mode asks a sharper question than "is this writable by somebody".
	//
	// The collector runs as root and hooks run as the logged-in user, so the check that matters is
	// whether a *non-root* process can write here. mode&0222 cannot answer that: it is satisfied by
	// the owner's write bit, so a root-owned 0600 log -- which no hook can write -- passed as OK.
	// The Windows branch above was added for exactly this reason; the POSIX branch had the same
	// hole and nobody had looked.
	//
	// Both parts have to hold. The file needs a world-write bit, and the directory needs to be
	// traversable, because a hook that cannot enter the directory never gets as far as the file.
	if !userMode {
		if mode&0002 == 0 {
			return Check{Name: "runtime_log_permissions", Target: path, Status: StatusFail, Severity: SeverityHigh,
				Message:  fmt.Sprintf("runtime log is mode %o, so hooks running as the logged-in user cannot append to it and their events are lost", mode),
				Evidence: "not_writable_by_hooks", Action: "sudo chmod 0666 " + path}
		}
		if dir := filepath.Dir(path); dir != "" {
			if dirInfo, dirErr := os.Stat(dir); dirErr == nil {
				if dirMode := dirInfo.Mode().Perm(); dirMode&0001 == 0 || dirMode&0004 == 0 {
					return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail, Severity: SeverityHigh,
						Message:  fmt.Sprintf("log directory is mode %o, so hooks running as the logged-in user cannot reach the runtime log inside it", dirMode),
						Evidence: "log_dir_not_traversable", Action: "sudo chmod 0755 " + dir}
				}
			}
		}
	}
	// A user-mode log is private on purpose: Beacon creates it 0600, since it holds that user's
	// prompts and command output and every process that writes it runs as them. Owner-only is the
	// healthy state there, not a readability problem.
	if userMode && mode&0200 != 0 {
		return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK, Severity: SeverityInfo, Message: fmt.Sprintf("mode %o", mode), Evidence: fmt.Sprintf("mode_%o", mode)}
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
			// Distinct from the POSIX not_writable on purpose. Evidence strings are what decide the
			// remediation doctor prints and what fleet tooling keys on, and the two have nothing in
			// common but the symptom: one is fixed by chmod, which does not exist here.
			Evidence: "acl_not_writable_by_user"}
	}
	_ = f.Close()
	return Check{Name: "runtime_log_permissions", Target: path, Status: StatusOK,
		Severity: SeverityInfo, Message: "the runtime log is writable by this user",
		Evidence: "writable_by_owner"}
}

// checkLogACL is the system-mode Windows form of the same question: can the people whose sessions
// this endpoint captures append to its log -- and can they do anything more?
//
// The mode-bit check above cannot answer it there. Windows reports 0666 for any ordinary file
// regardless of its ACL, so `mode&0222 == 0` is never true and the check would report OK for
// exactly the configuration it exists to catch -- a %ProgramData% log that only administrators can
// write, with every hook write failing silently.
//
// The second half matters as much. An endpoint installed before the log ACL was narrowed gives
// interactive users Modify on the directory, which lets any of them delete or rename the log and
// plant links a SYSTEM write follows. That endpoint captures everything, so nothing else would
// ever flag it; this does, and doctor --fix applies the same ACL a fresh install does.
//
// Read from the ACL rather than by attempting a write, because doctor usually runs elevated: a
// test write would succeed for the very user whose access is not in question.
func checkLogACL(path string) Check {
	dir := filepath.Dir(path)
	state, err := writer.CheckSystemLogACL(path)
	switch {
	case err != nil:
		// Unknown is reported as unknown. Claiming either answer here would be worse than saying
		// the check could not run.
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusWarn,
			Severity: SeverityLow, Message: "could not read the log directory ACL: " + err.Error(),
			Evidence: "acl_unreadable"}
	case len(state.Problems) > 0:
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail,
			Severity: SeverityHigh,
			Message: "the log directory ACL lets ordinary users do more than append, so one could " +
				"redirect a SYSTEM write: " + strings.Join(state.Problems, "; "),
			Evidence: "acl_too_broad"}
	case !state.HooksCanAppend:
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusFail,
			Severity: SeverityHigh,
			Message: "interactive users cannot append to the runtime log, so agent hooks will " +
				"fail silently while the collector reports healthy",
			Evidence: "acl_missing_interactive_write"}
	default:
		return Check{Name: "runtime_log_permissions", Target: dir, Status: StatusOK,
			Severity: SeverityInfo, Message: "interactive users may append to the runtime log, and " +
				"only SYSTEM and Administrators may change the log directory",
			Evidence: "acl_interactive_write"}
	}
}

// SecretFiles lists the system files that hold a HEC token when the endpoint has a Splunk or Falcon
// destination: config.json and the collector config.
func SecretFiles(cfg endpointconfig.Config) []string {
	return []string{endpointconfig.ConfigPath(false), cfg.Collector.ConfigPath}
}

// checkSecretFilesPrivate asks of a Windows system endpoint with a HEC destination whether its
// token is readable only by SYSTEM and Administrators. An endpoint installed before those files
// were written private lets every local user read them, because the machine-wide tree grants Users
// read and a 0600 mode changes nothing there; nothing else would notice, since the endpoint works.
// inspect is sysacl.InspectPrivateFile, a seam for tests.
func checkSecretFilesPrivate(cfg endpointconfig.Config, inspect func(string) ([]string, error)) Check {
	var problems []string
	for _, path := range SecretFiles(cfg) {
		found, err := inspect(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Check{Name: "secret_files_private", Target: path, Status: StatusWarn, Severity: SeverityLow,
				Message: "could not read the access control list: " + err.Error(), Evidence: "acl_unreadable"}
		}
		for _, p := range found {
			problems = append(problems, path+": "+p)
		}
	}
	if len(problems) > 0 {
		return Check{Name: "secret_files_private", Target: filepath.Dir(endpointconfig.ConfigPath(false)), Status: StatusFail,
			Severity: SeverityHigh,
			Message:  "a file holding the HEC token is readable by accounts other than SYSTEM and Administrators: " + strings.Join(problems, "; "),
			Evidence: "secret_readable_by_users"}
	}
	return Check{Name: "secret_files_private", Target: filepath.Dir(endpointconfig.ConfigPath(false)), Status: StatusOK,
		Severity: SeverityInfo, Message: "the files holding the HEC token are readable only by SYSTEM and Administrators",
		Evidence: "secret_private"}
}
