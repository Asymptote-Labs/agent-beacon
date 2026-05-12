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
	SystemLabel = "com.beacon.endpoint.collector"
	UserLabel   = "com.beacon.endpoint.collector.user"
)

type Manager struct {
	UserMode bool
}

type Status struct {
	Label   string `json:"label"`
	Loaded  bool   `json:"loaded"`
	Running bool   `json:"running"`
	Message string `json:"message,omitempty"`
}

func (m Manager) Label() string {
	if m.UserMode {
		return UserLabel
	}
	return SystemLabel
}

func (m Manager) PlistPath() (string, error) {
	if m.UserMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"), nil
	}
	return filepath.Join("/Library/LaunchDaemons", SystemLabel+".plist"), nil
}

func (m Manager) WritePlist(program, configPath string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("launchd service management is supported only on macOS")
	}
	path, err := m.PlistPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	content := plist(m.Label(), program, configPath)
	return path, os.WriteFile(path, []byte(content), 0644)
}

func (m Manager) Load() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path, err := m.PlistPath()
	if err != nil {
		return err
	}
	if m.UserMode {
		return runLaunchctl("bootstrap", "gui/"+fmt.Sprint(os.Getuid()), path)
	}
	return runLaunchctl("bootstrap", "system", path)
}

func (m Manager) Unload() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if m.UserMode {
		return runLaunchctl("bootout", "gui/"+fmt.Sprint(os.Getuid())+"/"+m.Label())
	}
	return runLaunchctl("bootout", "system/"+m.Label())
}

func (m Manager) Status() Status {
	status := Status{Label: m.Label()}
	if runtime.GOOS != "darwin" {
		status.Message = "service status is available only on macOS"
		return status
	}
	out, err := exec.Command("launchctl", "print", serviceDomain(m.UserMode)+"/"+m.Label()).CombinedOutput()
	if err != nil {
		status.Message = strings.TrimSpace(string(out))
		return status
	}
	status.Loaded = true
	text := string(out)
	status.Running = strings.Contains(text, "state = running") || strings.Contains(text, "pid =")
	return status
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	if strings.Contains(text, "already bootstrapped") || strings.Contains(text, "No such process") {
		return nil
	}
	return fmt.Errorf("launchctl %s failed: %s: %w", strings.Join(args, " "), text, err)
}

func serviceDomain(userMode bool) string {
	if userMode {
		return "gui/" + fmt.Sprint(os.Getuid())
	}
	return "system"
}

func plist(label, program, configPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/%s.out</string>
  <key>StandardErrorPath</key>
  <string>/tmp/%s.err</string>
</dict>
</plist>
`, label, program, configPath, label, label)
}
