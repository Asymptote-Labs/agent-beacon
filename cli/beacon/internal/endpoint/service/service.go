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

var runLaunchctlCommand = func(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
	domain := serviceDomain(m.UserMode)
	out, err := runLaunchctlCommand("bootstrap", domain, path)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out)
	if strings.Contains(text, "already bootstrapped") {
		target := domain + "/" + m.Label()
		if err := runLaunchctlWithContext(domain, m.Label(), "", "bootout", target); err != nil {
			return err
		}
		return runLaunchctlWithContext(domain, m.Label(), path, "bootstrap", domain, path)
	}
	return launchctlError(text, err, domain, m.Label(), path, "bootstrap", domain, path)
}

func (m Manager) Unload() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	target := serviceDomain(m.UserMode) + "/" + m.Label()
	return runLaunchctlWithContext(serviceDomain(m.UserMode), m.Label(), "", "bootout", target)
}

func (m Manager) Status() Status {
	status := Status{Label: m.Label()}
	if runtime.GOOS != "darwin" {
		status.Message = "service status is available only on macOS"
		return status
	}
	out, err := runLaunchctlCommand("print", serviceDomain(m.UserMode)+"/"+m.Label())
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	text := out
	status.Running = strings.Contains(text, "state = running") || strings.Contains(text, "pid =")
	return status
}

func runLaunchctl(args ...string) error {
	return runLaunchctlWithContext("", "", "", args...)
}

func runLaunchctlWithContext(domain, label, plistPath string, args ...string) error {
	out, err := runLaunchctlCommand(args...)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out)
	if strings.Contains(text, "No such process") {
		return nil
	}
	return launchctlError(text, err, domain, label, plistPath, args...)
}

func launchctlError(text string, err error, domain, label, plistPath string, args ...string) error {
	context := launchctlContext(domain, label, plistPath)
	guidance := launchctlGuidance(text, domain, label)
	if guidance != "" {
		return fmt.Errorf("launchctl %s failed%s: %s: %w\n%s", strings.Join(args, " "), context, text, err, guidance)
	}
	return fmt.Errorf("launchctl %s failed%s: %s: %w", strings.Join(args, " "), context, text, err)
}

func launchctlContext(domain, label, plistPath string) string {
	var fields []string
	if label != "" {
		fields = append(fields, "label "+label)
	}
	if domain != "" {
		fields = append(fields, "domain "+domain)
	}
	if plistPath != "" {
		fields = append(fields, "plist "+plistPath)
	}
	if len(fields) == 0 {
		return ""
	}
	return " (" + strings.Join(fields, ", ") + ")"
}

func launchctlGuidance(output, domain, label string) string {
	if !strings.Contains(output, "Bootstrap failed: 5") && !strings.Contains(output, "Input/output error") {
		return ""
	}
	target := label
	if domain != "" && label != "" {
		target = domain + "/" + label
	}
	if target == "" {
		target = "the Beacon launchd job"
	}
	return fmt.Sprintf("Bootstrap failed: 5 usually means launchd could not read or execute the job. Verify the collector binary referenced by the plist exists and is executable, clear stale state with `launchctl bootout %s`, then inspect launchd logs with `log show --predicate 'process == \"launchd\"' --last 5m`.", target)
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
