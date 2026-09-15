package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// InventoryLabel is the launchd label of the scheduled inventory heartbeat job. It is a
	// LaunchAgent for a user-mode endpoint and a LaunchDaemon for a system-mode one.
	InventoryLabel = "com.beacon.endpoint.inventory"
	// InventoryServiceUnit and InventoryTimerUnit are the systemd equivalents. systemd splits
	// "what to run" from "when to run it", so a scheduled job needs both.
	InventoryServiceUnit = "beacon-inventory.service"
	InventoryTimerUnit   = "beacon-inventory.timer"

	// InventoryIntervalEnv overrides the heartbeat interval, in seconds. It exists for tests and
	// smoke checks; a fleet should run the default.
	InventoryIntervalEnv     = "BEACON_INVENTORY_INTERVAL_SECONDS"
	defaultInventoryInterval = 6 * time.Hour
)

// InventoryInterval is how often the scheduled heartbeat runs. The plist, the systemd timer and
// the status line all read it here so they can never disagree.
func InventoryInterval() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(InventoryIntervalEnv)); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultInventoryInterval
}

// InventoryManager manages the scheduled job that writes inventory heartbeats. It is a one-shot
// job on a timer, like the updater, but unlike the updater it exists in both user and system
// modes, like the forwarder, because a per-user Beacon install inventories that user's runtimes.
//
// The job fires once when loaded (install, boot, package upgrade) and then every
// InventoryInterval. There is no jitter: launchd measures StartInterval from the moment the job
// was loaded, and systemd's OnUnitActiveSec from the last run, so a fleet installed at different
// times is naturally staggered.
type InventoryManager struct {
	UserMode bool
	// Kind selects the backend; the zero value auto-detects.
	Kind Kind
}

func (m InventoryManager) resolvedKind() Kind {
	if m.Kind != KindAuto {
		return m.Kind
	}
	return DetectKind()
}

// Supported reports whether a scheduled inventory job can be installed here.
//
// Supervised mode has no scheduler, so there is deliberately no fallback: a timer that silently
// never fires would be worse than refusing to install one.
func (m InventoryManager) Supported() bool {
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
func (m InventoryManager) UnsupportedReason() string {
	switch m.resolvedKind() {
	case KindLaunchd:
		return "launchd service management is supported only on macOS"
	case KindSystemd:
		return "systemd is not PID 1 on this host, so the scheduled inventory job cannot be installed"
	default:
		return "the scheduled inventory job needs launchd or systemd; a supervised collector has no scheduler. " +
			"Run `beacon endpoint inventory heartbeat` from your own scheduler."
	}
}

// Label is the service identifier for status output.
func (m InventoryManager) Label() string {
	if m.resolvedKind() == KindSystemd {
		return InventoryTimerUnit
	}
	return InventoryLabel
}

// UnitPath returns where the job's service definition lives. On systemd this is the timer,
// since that is the unit an administrator enables and inspects.
func (m InventoryManager) UnitPath() (string, error) {
	switch m.resolvedKind() {
	case KindSystemd:
		if m.UserMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, ".config", "systemd", "user", InventoryTimerUnit), nil
		}
		return filepath.Join("/etc/systemd/system", InventoryTimerUnit), nil
	default:
		if m.UserMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, "Library", "LaunchAgents", InventoryLabel+".plist"), nil
		}
		return filepath.Join("/Library/LaunchDaemons", InventoryLabel+".plist"), nil
	}
}

// UnitPaths returns every file WriteUnit installs: the timer and the oneshot service on systemd,
// the plist on launchd. Removal sites must use this, not UnitPath, or the service unit is left
// behind (the updater made exactly that mistake).
func (m InventoryManager) UnitPaths() []string {
	path, err := m.UnitPath()
	if err != nil {
		return nil
	}
	if m.resolvedKind() == KindSystemd {
		return []string{path, filepath.Join(filepath.Dir(path), InventoryServiceUnit)}
	}
	return []string{path}
}

// RemoveUnits deletes every file WriteUnit installed. Missing files are not an error.
func (m InventoryManager) RemoveUnits() {
	for _, path := range m.UnitPaths() {
		_ = os.Remove(path)
	}
}

// inventoryJobArgs is the argument vector the scheduler invokes, after the program itself.
func inventoryJobArgs(userMode bool, logPath string) []string {
	args := []string{"endpoint", "inventory", "heartbeat", "--scheduled"}
	if userMode {
		args = append(args, "--user")
	} else {
		args = append(args, "--system")
	}
	if strings.TrimSpace(logPath) != "" {
		args = append(args, "--log-path", logPath)
	}
	return args
}

// WriteUnit installs the job definition that runs `program endpoint inventory heartbeat
// --scheduled` for this mode. logPath, when set, pins the runtime log the inventory log sits
// beside; empty means the mode's default.
func (m InventoryManager) WriteUnit(program, logPath string) (string, error) {
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
	args := inventoryJobArgs(m.UserMode, logPath)
	if m.resolvedKind() == KindSystemd {
		svc := filepath.Join(filepath.Dir(path), InventoryServiceUnit)
		if err := os.WriteFile(svc, []byte(inventoryServiceUnit(program, args, m.UserMode)), 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(inventoryTimerUnit(InventoryInterval())), 0o644); err != nil {
			return "", err
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "daemon-reload")...); err != nil {
			return path, systemctlError(out, err, "daemon-reload")
		}
		return path, nil
	}
	content := inventoryPlist(InventoryLabel, program, args, InventoryInterval())
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Load activates the schedule. Loading fires the job once (RunAtLoad on launchd, an already
// elapsed OnBootSec on systemd), so every install, repair and package upgrade writes a heartbeat
// right away. An already-loaded launchd job is re-bootstrapped so a rewritten unit takes effect;
// the job exits in about a second, so there is nothing to wait for.
func (m InventoryManager) Load() error {
	if !m.Supported() {
		return fmt.Errorf("%s", m.UnsupportedReason())
	}
	if m.resolvedKind() == KindSystemd {
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "enable", "--now", InventoryTimerUnit)...); err != nil {
			return systemctlError(out, err, "enable --now "+InventoryTimerUnit)
		}
		return nil
	}
	path, err := m.UnitPath()
	if err != nil {
		return err
	}
	return loadLaunchdJob(serviceDomain(m.UserMode), InventoryLabel, path)
}

// Unload deactivates the schedule. A missing unit is not an error.
func (m InventoryManager) Unload() error {
	switch m.resolvedKind() {
	case KindSystemd:
		if !systemdIsInit() {
			return nil
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "stop", InventoryTimerUnit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "stop "+InventoryTimerUnit)
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "disable", InventoryTimerUnit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "disable "+InventoryTimerUnit)
		}
		return nil
	case KindLaunchd:
		if runtime.GOOS != "darwin" {
			return nil
		}
		domain := serviceDomain(m.UserMode)
		return runLaunchctlWithContext(domain, InventoryLabel, "", "bootout", domain+"/"+InventoryLabel)
	default:
		return nil
	}
}

// Status reports whether the schedule is active. For a one-shot job "running" means the timer
// is armed (systemd) or the job is registered with launchd; it does not mean a heartbeat is
// being written this instant.
func (m InventoryManager) Status() Status {
	kind := m.resolvedKind()
	if !m.Supported() {
		return Status{Label: m.Label(), Kind: string(kind), Message: m.UnsupportedReason()}
	}
	if kind == KindSystemd {
		status := Status{Label: InventoryTimerUnit, Kind: string(KindSystemd)}
		enabledOut, _ := runSystemctlCommand(systemctlArgs(m.UserMode, "is-enabled", InventoryTimerUnit)...)
		switch strings.TrimSpace(enabledOut) {
		case "enabled", "enabled-runtime", "static":
			status.Loaded = true
		}
		activeOut, _ := runSystemctlCommand(systemctlArgs(m.UserMode, "is-active", InventoryTimerUnit)...)
		status.Running = strings.TrimSpace(activeOut) == "active"
		if status.Running {
			status.Loaded = true
		}
		if !status.Loaded && !status.Running {
			status.Message = "scheduled inventory job not installed"
		}
		return status
	}
	status := Status{Label: InventoryLabel, Kind: string(KindLaunchd)}
	out, err := runLaunchctlCommand("print", serviceDomain(m.UserMode)+"/"+InventoryLabel)
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

// inventoryServiceUnit is the oneshot job the timer triggers.
func inventoryServiceUnit(program string, args []string, userMode bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Beacon endpoint inventory heartbeat\n")
	b.WriteString("Documentation=https://docs.asymptotelabs.ai/cli/endpoint-inventory\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
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

// inventoryTimerUnit schedules the job: shortly after the timer starts (so enabling it after
// boot fires at once), then every interval. Persistent=true runs a firing missed while asleep.
func inventoryTimerUnit(interval time.Duration) string {
	return fmt.Sprintf(`[Unit]
Description=Beacon endpoint inventory heartbeat

[Timer]
OnBootSec=2min
OnUnitActiveSec=%ds
Persistent=true
Unit=%s

[Install]
WantedBy=timers.target
`, int(interval.Seconds()), InventoryServiceUnit)
}

// inventoryPlist renders the launchd job: one shot at load, then every interval. No KeepAlive,
// so launchd does not restart a job that exited normally.
func inventoryPlist(label, program string, args []string, interval time.Duration) string {
	var argv strings.Builder
	fmt.Fprintf(&argv, "    <string>%s</string>\n", xmlEscape(program))
	for _, arg := range args {
		fmt.Fprintf(&argv, "    <string>%s</string>\n", xmlEscape(arg))
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
  <key>RunAtLoad</key>
  <true/>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>StandardOutPath</key>
  <string>/tmp/%s.out</string>
  <key>StandardErrorPath</key>
  <string>/tmp/%s.err</string>
</dict>
</plist>
`, xmlEscape(label), argv.String(), int(interval.Seconds()), xmlEscape(label), xmlEscape(label))
}
