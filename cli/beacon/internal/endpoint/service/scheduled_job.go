package service

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// jobSpec names one scheduled one-shot job: what launchd and systemd call it, what it runs, and
// how often. The inventory heartbeat and the session sync are both one of these; everything else
// about installing, loading and reporting them is shared.
type jobSpec struct {
	// label is the launchd label: a LaunchAgent in user mode, a LaunchDaemon in system mode.
	label string
	// serviceUnit and timerUnit are the systemd pair; systemd splits what to run from when.
	serviceUnit string
	timerUnit   string
	// description is the systemd Description ("Beacon endpoint <description>").
	description string
	// name is how messages refer to the job, for example "scheduled inventory job".
	name string
	// docs is the systemd Documentation URL.
	docs string
	// manualHint is what UnsupportedReason tells someone on a host with no scheduler to run instead.
	manualHint string
	// interval is how often the job runs.
	interval func() time.Duration
	// runAtLoad fires the job once when it is loaded (install, boot, package upgrade) as well as
	// every interval. Without it the first run is one interval after loading.
	runAtLoad bool
	// args is the argument vector after the program, for this mode and runtime log.
	args func(userMode bool, logPath string) []string
}

// intervalFromEnv reads a positive number of seconds from env, falling back to def.
func intervalFromEnv(env string, def time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(env)); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}

// ScheduledJob is what callers ask of a scheduled job's manager, InventoryManager or
// SessionSyncManager: install and repair reconcile one through it, and their dry-run plan asks it
// whether the host can schedule the job and where its unit goes.
type ScheduledJob interface {
	Supported() bool
	UnsupportedReason() string
	UnitPath() (string, error)
	UnitPaths() []string
	WriteUnit(program, logPath string) (string, error)
	Load() error
	Unload() error
	RemoveUnits()
	Status() Status
}

// scheduledJob is one jobSpec installed in one mode. It is a one-shot job on a timer, like the
// updater, but it exists in both user and system modes, like the forwarder.
//
// The job runs every interval, and with spec.runAtLoad also once when loaded (install, boot,
// package upgrade). There is no jitter: launchd measures StartInterval from the moment the job was
// loaded, and systemd's OnUnitActiveSec from the last run, so a fleet installed at different times
// is naturally staggered.
type scheduledJob struct {
	spec     jobSpec
	userMode bool
	kind     Kind
	// env is set in the job's environment. launchd and systemd start the job without the
	// installing user's shell environment, so anything the job reads from it has to be written
	// into the unit.
	env map[string]string
}

func (m scheduledJob) resolvedKind() Kind {
	if m.kind != KindAuto {
		return m.kind
	}
	return DetectKind()
}

// Supported reports whether the scheduled job can be installed here.
//
// Supervised mode has no scheduler, so there is deliberately no fallback: a timer that silently
// never fires would be worse than refusing to install one.
func (m scheduledJob) Supported() bool {
	switch m.resolvedKind() {
	case KindLaunchd:
		return runtime.GOOS == "darwin"
	case KindSystemd:
		return systemdIsInit()
	default:
		return false
	}
}

// UnsupportedReason explains why Supported is false.
func (m scheduledJob) UnsupportedReason() string {
	switch m.resolvedKind() {
	case KindLaunchd:
		return "launchd service management is supported only on macOS"
	case KindSystemd:
		return "systemd is not PID 1 on this host, so the " + m.spec.name + " cannot be installed"
	default:
		return "the " + m.spec.name + " needs launchd or systemd; a supervised collector has no scheduler. " + m.spec.manualHint
	}
}

// Label is the service identifier for status output.
func (m scheduledJob) Label() string {
	if m.resolvedKind() == KindSystemd {
		return m.spec.timerUnit
	}
	return m.spec.label
}

// UnitPath returns where the job's service definition lives. On systemd this is the timer,
// since that is the unit an administrator enables and inspects.
func (m scheduledJob) UnitPath() (string, error) {
	switch m.resolvedKind() {
	case KindSystemd:
		if m.userMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, ".config", "systemd", "user", m.spec.timerUnit), nil
		}
		return filepath.Join("/etc/systemd/system", m.spec.timerUnit), nil
	default:
		if m.userMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, "Library", "LaunchAgents", m.spec.label+".plist"), nil
		}
		return filepath.Join("/Library/LaunchDaemons", m.spec.label+".plist"), nil
	}
}

// UnitPaths returns every file WriteUnit installs: the timer and the oneshot service on systemd,
// the plist on launchd. Removal sites must use this, not UnitPath, or the service unit is left
// behind (the updater made exactly that mistake).
func (m scheduledJob) UnitPaths() []string {
	path, err := m.UnitPath()
	if err != nil {
		return nil
	}
	if m.resolvedKind() == KindSystemd {
		return []string{path, filepath.Join(filepath.Dir(path), m.spec.serviceUnit)}
	}
	return []string{path}
}

// RemoveUnits deletes every file WriteUnit installed. Missing files are not an error.
func (m scheduledJob) RemoveUnits() {
	for _, path := range m.UnitPaths() {
		_ = os.Remove(path)
	}
}

// WriteUnit installs the job definition that runs program with the spec's arguments for this
// mode. logPath, when set, pins the runtime log; empty means the mode's default.
func (m scheduledJob) WriteUnit(program, logPath string) (string, error) {
	if !m.Supported() {
		return "", fmt.Errorf("%s", m.UnsupportedReason())
	}
	path, err := m.UnitPath()
	if err != nil {
		return "", err
	}
	if err := ensureDir(path); err != nil {
		return "", err
	}
	args := m.spec.args(m.userMode, logPath)
	if m.resolvedKind() == KindSystemd {
		svc := filepath.Join(filepath.Dir(path), m.spec.serviceUnit)
		if err := os.WriteFile(svc, []byte(jobServiceUnit(m.spec, program, args, m.userMode, m.env)), 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(jobTimerUnit(m.spec, m.spec.interval())), 0o644); err != nil {
			return "", err
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.userMode, "daemon-reload")...); err != nil {
			return path, systemctlError(out, err, "daemon-reload")
		}
		return path, nil
	}
	if err := ensureLaunchdLogDir(m.userMode, m.spec.label); err != nil {
		return "", err
	}
	stdout, stderr, err := launchdLogPaths(m.userMode, m.spec.label)
	if err != nil {
		return "", err
	}
	content := jobPlist(m.spec.label, program, args, m.spec.interval(), m.spec.runAtLoad, stdout, stderr, m.env)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Load activates the schedule. With spec.runAtLoad, loading fires the job once (RunAtLoad on
// launchd, an already elapsed OnBootSec on systemd). An already-loaded launchd job is
// re-bootstrapped so a rewritten unit takes effect.
func (m scheduledJob) Load() error {
	if !m.Supported() {
		return fmt.Errorf("%s", m.UnsupportedReason())
	}
	if m.resolvedKind() == KindSystemd {
		if out, err := runSystemctlCommand(systemctlArgs(m.userMode, "enable", "--now", m.spec.timerUnit)...); err != nil {
			return systemctlError(out, err, "enable --now "+m.spec.timerUnit)
		}
		return nil
	}
	path, err := m.UnitPath()
	if err != nil {
		return err
	}
	return loadLaunchdJob(serviceDomain(m.userMode), m.spec.label, path)
}

// Unload deactivates the schedule. A missing unit is not an error.
func (m scheduledJob) Unload() error {
	switch m.resolvedKind() {
	case KindSystemd:
		if !systemdIsInit() {
			return nil
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.userMode, "stop", m.spec.timerUnit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "stop "+m.spec.timerUnit)
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.userMode, "disable", m.spec.timerUnit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "disable "+m.spec.timerUnit)
		}
		return nil
	case KindLaunchd:
		if runtime.GOOS != "darwin" {
			return nil
		}
		return bootoutLaunchdJob(serviceDomain(m.userMode), m.spec.label)
	default:
		return nil
	}
}

// Status reports whether the schedule is active. For a one-shot job "running" means the timer
// is armed (systemd) or the job is registered with launchd; it does not mean the job is running
// this instant.
func (m scheduledJob) Status() Status {
	kind := m.resolvedKind()
	if !m.Supported() {
		return Status{Label: m.Label(), Kind: string(kind), Message: m.UnsupportedReason()}
	}
	if kind == KindSystemd {
		status := Status{Label: m.spec.timerUnit, Kind: string(KindSystemd)}
		enabledOut, _ := runSystemctlCommand(systemctlArgs(m.userMode, "is-enabled", m.spec.timerUnit)...)
		switch strings.TrimSpace(enabledOut) {
		case "enabled", "enabled-runtime", "static":
			status.Loaded = true
		}
		activeOut, _ := runSystemctlCommand(systemctlArgs(m.userMode, "is-active", m.spec.timerUnit)...)
		status.Running = strings.TrimSpace(activeOut) == "active"
		if status.Running {
			status.Loaded = true
		}
		if !status.Loaded && !status.Running {
			status.Message = m.spec.name + " not installed"
		}
		return status
	}
	status := Status{Label: m.spec.label, Kind: string(KindLaunchd)}
	out, err := runLaunchctlCommand("print", serviceDomain(m.userMode)+"/"+m.spec.label)
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

// jobServiceUnit is the oneshot job the timer triggers.
func jobServiceUnit(spec jobSpec, program string, args []string, userMode bool, env map[string]string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=Beacon endpoint %s\n", spec.description)
	fmt.Fprintf(&b, "Documentation=%s\n", spec.docs)
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
	for _, key := range slices.Sorted(maps.Keys(env)) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdArg(key+"="+env[key]))
	}
	fmt.Fprintf(&b, "ExecStart=%s", systemdArg(program))
	for _, arg := range args {
		fmt.Fprintf(&b, " %s", systemdArg(arg))
	}
	b.WriteString("\n")
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	if !userMode {
		// System mode writes /var/log/beacon-agent, which root owns.
		b.WriteString("User=root\n")
	}
	return b.String()
}

// jobTimerUnit schedules the job every interval. With spec.runAtLoad the first firing is shortly
// after boot (so enabling the timer after boot fires at once); without it, one interval after the
// timer starts. Persistent=true runs a firing missed while asleep.
func jobTimerUnit(spec jobSpec, interval time.Duration) string {
	first := "OnBootSec=2min"
	if !spec.runAtLoad {
		first = fmt.Sprintf("OnActiveSec=%ds", int(interval.Seconds()))
	}
	return fmt.Sprintf(`[Unit]
Description=Beacon endpoint %s

[Timer]
%s
OnUnitActiveSec=%ds
Persistent=true
Unit=%s

[Install]
WantedBy=timers.target
`, spec.description, first, int(interval.Seconds()), spec.serviceUnit)
}

// jobPlist renders the launchd job: every interval, and with runAtLoad one shot at load too. No
// KeepAlive, so launchd does not restart a job that exited normally.
func jobPlist(label, program string, args []string, interval time.Duration, runAtLoad bool, stdoutPath, stderrPath string, env map[string]string) string {
	var argv strings.Builder
	fmt.Fprintf(&argv, "    <string>%s</string>\n", xmlEscape(program))
	for _, arg := range args {
		fmt.Fprintf(&argv, "    <string>%s</string>\n", xmlEscape(arg))
	}
	var envBlock strings.Builder
	if len(env) > 0 {
		envBlock.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, key := range slices.Sorted(maps.Keys(env)) {
			fmt.Fprintf(&envBlock, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(key), xmlEscape(env[key]))
		}
		envBlock.WriteString("  </dict>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
%s  <key>RunAtLoad</key>
  <%t/>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(label), argv.String(), envBlock.String(), runAtLoad, int(interval.Seconds()), xmlEscape(stdoutPath), xmlEscape(stderrPath))
}
