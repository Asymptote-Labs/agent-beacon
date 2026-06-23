package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// UpdaterLabel is the launchd label for the periodic self-updater job. It is a
// system LaunchDaemon (runs as root) so it can apply package updates without a
// password prompt.
const UpdaterLabel = "com.beacon.endpoint.updater"

// UpdaterManager manages the self-updater launchd job. Unlike the collector
// service it is system-only and runs on a schedule rather than KeepAlive.
type UpdaterManager struct{}

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
	out, err := runLaunchctlCommand("bootstrap", "system", path)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.TrimSpace(out), "already bootstrapped") {
		target := "system/" + UpdaterLabel
		if err := runLaunchctlWithContext("system", UpdaterLabel, "", "bootout", target); err != nil {
			return err
		}
		return runLaunchctlWithContext("system", UpdaterLabel, path, "bootstrap", "system", path)
	}
	return launchctlError(strings.TrimSpace(out), err, "system", UpdaterLabel, path, "bootstrap", "system", path)
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

// updaterPlist renders the periodic-updater LaunchDaemon. It runs daily and does
// not RunAtLoad (so installing the package does not trigger an immediate update
// mid-install) and is not KeepAlive (it is a one-shot scheduled job). In-process
// jitter spreads fleet-wide checks; see the --scheduled handler.
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
    <integer>3</integer>
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
