package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// UpdaterLabel is the launchd label for the periodic endpoint updater job.
const UpdaterLabel = "com.beacon.endpoint.updater"

// UpdaterManager manages the endpoint updater launchd job. Unlike the collector
// service it is system-only and runs on a schedule rather than KeepAlive.
type UpdaterManager struct{}

var startDeferredUpdaterReload = func(path string) error {
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf(
		"sleep 75; /bin/launchctl bootout system/%s >/dev/null 2>&1 || true; /bin/launchctl bootstrap system %s >/dev/null 2>&1",
		UpdaterLabel,
		shellQuote(path),
	))
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

// PlistPath returns the LaunchDaemon plist path for the updater job.
func (UpdaterManager) PlistPath() string {
	return filepath.Join("/Library/LaunchDaemons", UpdaterLabel+".plist")
}

// WritePlist writes the updater LaunchDaemon plist invoking the given program.
func (m UpdaterManager) WritePlist(program string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("launchd service management is supported only on macOS")
	}
	path := m.PlistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(updaterPlist(UpdaterLabel, program)), 0644)
}

// Load bootstraps the updater job into the system domain.
func (m UpdaterManager) Load() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path := m.PlistPath()
	if status := m.Status(); status.Loaded && status.Running {
		return startDeferredUpdaterReload(path)
	}
	return loadLaunchdJob("system", UpdaterLabel, path)
}

// Unload boots the updater job out of the system domain.
func (m UpdaterManager) Unload() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return runLaunchctlWithContext("system", UpdaterLabel, "", "bootout", "system/"+UpdaterLabel)
}

// Status reports whether the updater job is loaded.
func (m UpdaterManager) Status() Status {
	status := Status{Label: UpdaterLabel}
	if runtime.GOOS != "darwin" {
		status.Message = "service status is available only on macOS"
		return status
	}
	out, err := runLaunchctlCommand("print", "system/"+UpdaterLabel)
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

// updaterPlist renders the updater LaunchDaemon. It runs daily at 2 PM local
// time and does not
// RunAtLoad or KeepAlive; each invocation resolves the configured mode.
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
  <dict>
    <key>Hour</key>
    <integer>14</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
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
