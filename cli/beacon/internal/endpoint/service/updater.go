package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// UpdaterLabel is the launchd label for the periodic endpoint updater job.
	UpdaterLabel = "com.beacon.endpoint.updater"
	// UpdaterTimerUnit and UpdaterServiceUnit are the systemd equivalents. systemd splits
	// "what to run" from "when to run it", so a scheduled job needs both.
	UpdaterServiceUnit = "beacon-updater.service"
	UpdaterTimerUnit   = "beacon-updater.timer"
)

// UpdaterManager manages the periodic endpoint updater. Unlike the collector it is
// system-only and runs on a schedule rather than staying resident.
type UpdaterManager struct {
	// Kind selects the backend; the zero value auto-detects.
	Kind Kind
}

func (m UpdaterManager) resolvedKind() Kind {
	if m.Kind != KindAuto {
		return m.Kind
	}
	return DetectKind()
}

// Supported reports whether a scheduled updater can be installed here.
//
// Supervised mode has no scheduler, so there is deliberately no fallback: a timer that
// silently never fires would be worse than refusing to install one.
func (m UpdaterManager) Supported() bool {
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
func (m UpdaterManager) UnsupportedReason() string {
	switch m.resolvedKind() {
	case KindLaunchd:
		return "launchd service management is supported only on macOS"
	case KindSystemd:
		return "systemd is not PID 1 on this host, so a scheduled updater cannot be installed"
	default:
		return "the scheduled updater needs launchd or systemd; a supervised collector has no scheduler. " +
			"Update Beacon through your package manager or run `beacon endpoint update --apply` from your own scheduler."
	}
}

var startDeferredUpdaterReload = func(path string) error {
	cmd := exec.Command("/bin/sh", "-c", deferredUpdaterReloadScript(path))
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

func deferredUpdaterReloadScript(path string) string {
	return fmt.Sprintf(
		"deadline=$((SECONDS+600)); while [ \"$SECONDS\" -lt \"$deadline\" ] && /bin/launchctl print system/%s 2>/dev/null | /usr/bin/grep -Eq 'state = running|pid ='; do sleep 2; done; /bin/launchctl bootout system/%s >/dev/null 2>&1 || true; /bin/launchctl bootstrap system %s >/dev/null 2>&1",
		UpdaterLabel,
		UpdaterLabel,
		shellQuote(path),
	)
}

// UnitPath returns the on-disk path of the updater service definition. On systemd this is the
// timer, since that is the unit an administrator enables and inspects.
func (m UpdaterManager) UnitPath() string {
	if m.resolvedKind() == KindSystemd {
		return filepath.Join("/etc/systemd/system", UpdaterTimerUnit)
	}
	return filepath.Join("/Library/LaunchDaemons", UpdaterLabel+".plist")
}

// UnitPaths returns every file WriteUnit installs, so a caller removing the updater removes all of
// it.
//
// UnitPath is not enough on systemd, and the asymmetry is easy to miss: WriteUnit writes both the
// timer and the oneshot service it triggers, while UnitPath deliberately names only the timer --
// the unit an administrator enables and inspects. Every removal site used UnitPath, so
// beacon-updater.service was left behind under /etc/systemd/system by uninstall and by disable.
func (m UpdaterManager) UnitPaths() []string {
	if m.resolvedKind() == KindSystemd {
		return []string{
			filepath.Join("/etc/systemd/system", UpdaterTimerUnit),
			filepath.Join("/etc/systemd/system", UpdaterServiceUnit),
		}
	}
	return []string{m.UnitPath()}
}

// RemoveUnits deletes every file WriteUnit installed.
//
// Exists so no caller has to remember that systemd needs two files removed and launchd one. The
// previous shape -- callers looping over UnitPaths themselves -- was better than callers using
// UnitPath, but it still left the knowledge duplicated at four sites, and one of them was missed:
// `reconcileUpdaterFromConfig` kept removing only the timer, so turning auto-update off left
// beacon-updater.service behind. A method that owns the whole set cannot be half-adopted.
func (m UpdaterManager) RemoveUnits() {
	for _, path := range m.UnitPaths() {
		_ = os.Remove(path)
	}
}

// WriteUnit installs the updater definition invoking the given program.
func (m UpdaterManager) WriteUnit(program string) (string, error) {
	if !m.Supported() {
		return "", fmt.Errorf("%s", m.UnsupportedReason())
	}
	if m.resolvedKind() == KindSystemd {
		return m.writeSystemdUnits(program)
	}
	path := m.UnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(updaterPlist(UpdaterLabel, program)), 0o644)
}

// writeSystemdUnits writes the oneshot service plus the timer that drives it.
func (m UpdaterManager) writeSystemdUnits(program string) (string, error) {
	dir := "/etc/systemd/system"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	svc := filepath.Join(dir, UpdaterServiceUnit)
	if err := os.WriteFile(svc, []byte(updaterServiceUnit(program)), 0o644); err != nil {
		return "", err
	}
	timer := filepath.Join(dir, UpdaterTimerUnit)
	if err := os.WriteFile(timer, []byte(updaterTimerUnit()), 0o644); err != nil {
		return "", err
	}
	if out, err := runSystemctlCommand("daemon-reload"); err != nil {
		return timer, systemctlError(out, err, "daemon-reload")
	}
	return timer, nil
}

// Load activates the updater schedule.
func (m UpdaterManager) Load() error {
	if !m.Supported() {
		return fmt.Errorf("%s", m.UnsupportedReason())
	}
	if m.resolvedKind() == KindSystemd {
		if out, err := runSystemctlCommand("enable", "--now", UpdaterTimerUnit); err != nil {
			return systemctlError(out, err, "enable --now "+UpdaterTimerUnit)
		}
		return nil
	}
	path := m.UnitPath()
	// A running updater must not be torn out from under itself mid-update, so the reload is
	// deferred until the current invocation finishes.
	if status := m.Status(); status.Loaded && status.Running {
		return startDeferredUpdaterReload(path)
	}
	return loadLaunchdJob("system", UpdaterLabel, path)
}

// Unload deactivates the updater schedule. A missing unit is not an error.
func (m UpdaterManager) Unload() error {
	switch m.resolvedKind() {
	case KindSystemd:
		if !systemdIsInit() {
			return nil
		}
		if out, err := runSystemctlCommand("stop", UpdaterTimerUnit); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "stop "+UpdaterTimerUnit)
		}
		if out, err := runSystemctlCommand("disable", UpdaterTimerUnit); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "disable "+UpdaterTimerUnit)
		}
		return nil
	case KindLaunchd:
		if runtime.GOOS != "darwin" {
			return nil
		}
		return runLaunchctlWithContext("system", UpdaterLabel, "", "bootout", "system/"+UpdaterLabel)
	default:
		return nil
	}
}

// Status reports whether the updater schedule is active.
func (m UpdaterManager) Status() Status {
	kind := m.resolvedKind()
	if !m.Supported() {
		label := UpdaterLabel
		if kind == KindSystemd {
			label = UpdaterTimerUnit
		}
		return Status{Label: label, Kind: string(kind), Message: m.UnsupportedReason()}
	}

	if kind == KindSystemd {
		status := Status{Label: UpdaterTimerUnit, Kind: string(KindSystemd)}
		enabledOut, _ := runSystemctlCommand("is-enabled", UpdaterTimerUnit)
		switch strings.TrimSpace(enabledOut) {
		case "enabled", "enabled-runtime", "static":
			status.Loaded = true
		}
		activeOut, _ := runSystemctlCommand("is-active", UpdaterTimerUnit)
		// A timer that is waiting for its next firing reports "active"; systemd does not
		// distinguish waiting from running for timer units.
		status.Running = strings.TrimSpace(activeOut) == "active"
		if status.Running {
			status.Loaded = true
		}
		if !status.Loaded && !status.Running {
			status.Message = "timer not installed"
		}
		return status
	}

	status := Status{Label: UpdaterLabel, Kind: string(KindLaunchd)}
	out, err := runLaunchctlCommand("print", "system/"+UpdaterLabel)
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

// updaterServiceUnit is the oneshot job the timer triggers.
func updaterServiceUnit(program string) string {
	return fmt.Sprintf(`[Unit]
Description=Beacon endpoint scheduled update check
Documentation=https://docs.asymptotelabs.ai/cli/endpoint

[Service]
Type=oneshot
ExecStart=%s endpoint update --scheduled
StandardOutput=journal
StandardError=journal
User=root
`, systemdArg(program))
}

// updaterTimerUnit schedules the job at the same business-hour intervals the launchd plist
// uses. Persistent=true is the meaningful addition: systemd will run a missed firing after a
// laptop wakes, whereas launchd's StartCalendarInterval simply skips it.
func updaterTimerUnit() string {
	return fmt.Sprintf(`[Unit]
Description=Beacon endpoint scheduled update check

[Timer]
OnCalendar=*-*-* 09,12,15,18,21:00:00
Persistent=true
Unit=%s

[Install]
WantedBy=timers.target
`, UpdaterServiceUnit)
}

// updaterPlist renders the updater LaunchDaemon. It runs at business-hour intervals in local
// time and does not RunAtLoad or KeepAlive; each invocation resolves the configured mode.
func updaterPlist(label, program string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>endpoint</string>
    <string>update</string>
    <string>--scheduled</string>
  </array>
  <key>StartCalendarInterval</key>
  <array>
  <dict>
    <key>Hour</key>
    <integer>9</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <dict>
    <key>Hour</key>
    <integer>12</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <dict>
    <key>Hour</key>
    <integer>15</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <dict>
    <key>Hour</key>
    <integer>18</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <dict>
    <key>Hour</key>
    <integer>21</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  </array>
  <key>RunAtLoad</key>
  <false/>
  <key>StandardOutPath</key>
  <string>/tmp/%s.out</string>
  <key>StandardErrorPath</key>
  <string>/tmp/%s.err</string>
</dict>
</plist>
`, label, program, label, label)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
