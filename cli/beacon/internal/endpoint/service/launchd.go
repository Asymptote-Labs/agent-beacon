package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// launchdBackend manages the collector via macOS launchd.
//
// This is the original implementation, unchanged in behavior; it moved here so that
// launchctl output parsing and launchd's error taxonomy no longer share a file with the
// platform-neutral types.
type launchdBackend struct{}

var runLaunchctlCommand = func(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (launchdBackend) kind() Kind { return KindLaunchd }

func (launchdBackend) available() bool { return runtime.GOOS == "darwin" }

func (launchdBackend) unsupportedReason() string {
	return "launchd service management is supported only on macOS"
}

func (launchdBackend) label(userMode bool) string {
	if userMode {
		return UserLabel
	}
	return SystemLabel
}

func (b launchdBackend) unitPath(userMode bool) (string, error) {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "LaunchAgents", UserLabel+".plist"), nil
	}
	return filepath.Join("/Library/LaunchDaemons", SystemLabel+".plist"), nil
}

func (b launchdBackend) writeUnit(userMode bool, program, configPath string) (string, error) {
	path, err := b.unitPath(userMode)
	if err != nil {
		return "", err
	}
	if err := ensureDir(path); err != nil {
		return "", err
	}
	content := plist(b.label(userMode), program, configPath)
	return path, os.WriteFile(path, []byte(content), 0o644)
}

func (b launchdBackend) load(userMode bool) error {
	path, err := b.unitPath(userMode)
	if err != nil {
		return err
	}
	return loadLaunchdJob(serviceDomain(userMode), b.label(userMode), path)
}

func (b launchdBackend) unload(userMode bool) error {
	domain := serviceDomain(userMode)
	target := domain + "/" + b.label(userMode)
	return runLaunchctlWithContext(domain, b.label(userMode), "", "bootout", target)
}

func (b launchdBackend) restart(userMode bool) error {
	domain := serviceDomain(userMode)
	label := b.label(userMode)
	target := domain + "/" + label
	out, err := runLaunchctlCommand("kickstart", "-k", target)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out)
	if launchctlNoSuchProcess(text) {
		return b.load(userMode)
	}
	return launchctlError(text, err, domain, label, "", "kickstart", "-k", target)
}

func (b launchdBackend) status(userMode bool) Status {
	status := Status{Label: b.label(userMode)}
	out, err := runLaunchctlCommand("print", serviceDomain(userMode)+"/"+b.label(userMode))
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

func runLaunchctlWithContext(domain, label, plistPath string, args ...string) error {
	out, err := runLaunchctlCommand(args...)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out)
	if launchctlNoSuchProcess(text) {
		return nil
	}
	return launchctlError(text, err, domain, label, plistPath, args...)
}

func launchctlNoSuchProcess(text string) bool {
	return strings.Contains(text, "No such process") || strings.Contains(text, "Could not find service")
}

func loadLaunchdJob(domain, label, plistPath string) error {
	out, err := runLaunchctlCommand("bootstrap", domain, plistPath)
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(out)
	if !launchdJobAppearsLoaded(text, domain, label) {
		return launchctlError(text, err, domain, label, plistPath, "bootstrap", domain, plistPath)
	}
	target := domain + "/" + label
	if err := runLaunchctlWithContext(domain, label, "", "bootout", target); err != nil {
		return err
	}
	if out, err := runLaunchctlCommand("bootstrap", domain, plistPath); err != nil {
		text := strings.TrimSpace(out)
		if launchdJobAppearsLoaded(text, domain, label) {
			return nil
		}
		return launchctlError(text, err, domain, label, plistPath, "bootstrap", domain, plistPath)
	}
	return nil
}

func launchdJobAppearsLoaded(bootstrapOutput, domain, label string) bool {
	text := strings.TrimSpace(bootstrapOutput)
	if strings.Contains(text, "already bootstrapped") {
		return true
	}
	if !strings.Contains(text, "Bootstrap failed: 5") && !strings.Contains(text, "Input/output error") {
		return false
	}
	out, err := runLaunchctlCommand("print", domain+"/"+label)
	return err == nil && strings.TrimSpace(out) != ""
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
