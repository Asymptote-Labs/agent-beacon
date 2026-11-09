package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ForwarderLabel is the launchd label of the Asymptote managed-ingest forwarder. It is a
	// LaunchAgent for a user-mode endpoint and a LaunchDaemon for a system-mode one.
	ForwarderLabel = "com.beacon.endpoint.asymptote-forwarder"
	// ForwarderSystemdUnit is the systemd equivalent, under the user or system scope.
	ForwarderSystemdUnit = "beacon-asymptote-forwarder.service"
)

// forwarderStartTimeout bounds the wait for a Windows forwarder to reach RUNNING after a start.
const forwarderStartTimeout = 60 * time.Second

// ForwarderManager manages a long-running Vector process that ships Beacon JSONL. It follows
// UpdaterManager's shape (unit write/load/unload/status) but is resident like the collector rather
// than scheduled. The Asymptote forwarder exists in both user and system modes, because a per-user
// Beacon install forwards that user's log.
type ForwarderManager struct {
	UserMode bool
	// Kind selects the backend; the zero value auto-detects.
	Kind Kind
	// Spec names the forwarder. The zero value is the Asymptote managed-ingest forwarder.
	Spec ForwarderSpec
}

// ForwarderSpec is one forwarder's identity under each service manager, and how Vector is run.
type ForwarderSpec struct {
	// Label is the launchd label.
	Label string
	// SystemdUnit is the systemd unit name.
	SystemdUnit string
	// WindowsService is the Service Control Manager name. Empty means the forwarder is not offered
	// on Windows.
	WindowsService string
	// Description names the forwarder in the systemd unit and the Windows service.
	Description string
	// Documentation is the systemd Documentation= URL.
	Documentation string
	// Args follow `--config <path>` on Vector's command line.
	Args []string
	// Env is added to Vector's environment, as NAME=value entries in a fixed order.
	Env []string
	// StopTimeout bounds a Windows stop; zero means serviceStopTimeout. It has to outlast Vector's
	// own drain limit, or a stop gives up while Vector is still flushing.
	StopTimeout time.Duration
}

// AsymptoteForwarderSpec is the Asymptote managed-ingest forwarder. It has no Windows service:
// connect stays refused there.
func AsymptoteForwarderSpec() ForwarderSpec {
	return ForwarderSpec{
		Label:         ForwarderLabel,
		SystemdUnit:   ForwarderSystemdUnit,
		Description:   "Beacon endpoint forwarder to Asymptote managed ingest",
		Documentation: "https://docs.asymptotelabs.ai/cli/endpoint-connect",
	}
}

func (m ForwarderManager) spec() ForwarderSpec {
	if m.Spec.Label == "" && m.Spec.SystemdUnit == "" && m.Spec.WindowsService == "" {
		return AsymptoteForwarderSpec()
	}
	return m.Spec
}

func (m ForwarderManager) scmService(vectorBin, configPath string) scmService {
	spec := m.spec()
	return scmService{
		Name:        spec.WindowsService,
		DisplayName: spec.Description,
		Description: spec.Description,
		Program:     vectorBin,
		Args:        append([]string{"--config", configPath}, spec.Args...),
		Env:         spec.Env,
		OwnsEnv:     true,
	}
}

func (m ForwarderManager) stopTimeout() time.Duration {
	if timeout := m.spec().StopTimeout; timeout > 0 {
		return timeout
	}
	return serviceStopTimeout
}

func (m ForwarderManager) resolvedKind() Kind {
	if m.Kind != KindAuto {
		return m.Kind
	}
	return DetectKind()
}

// Supported reports whether a supervised forwarder can be installed here.
//
// Supervised mode is deliberately not offered: a forwarder nothing restarts would silently stop
// shipping after the first crash or reboot, which is worse than telling the operator to run the
// pack's vector.toml under their own supervisor.
func (m ForwarderManager) Supported() bool {
	switch m.resolvedKind() {
	case KindLaunchd:
		return runtime.GOOS == "darwin"
	case KindSystemd:
		return systemdIsInit()
	case KindWindowsService:
		// System mode only: Windows has no per-user service manager.
		return runtime.GOOS == "windows" && !m.UserMode && m.spec().WindowsService != ""
	default:
		return false
	}
}

// UnsupportedReason explains why Supported is false.
func (m ForwarderManager) UnsupportedReason() string {
	switch m.resolvedKind() {
	case KindLaunchd:
		return "launchd service management is supported only on macOS"
	case KindSystemd:
		return "systemd is not PID 1 on this host, so the forwarder cannot be installed as a service"
	case KindWindowsService:
		switch {
		case m.spec().WindowsService == "":
			// The Asymptote forwarder; keeps the message it has always had.
		case runtime.GOOS != "windows":
			return "Windows service management is available only on Windows"
		case m.UserMode:
			return "Windows has no per-user service manager, so the forwarder runs only with a system install; use --system from an elevated shell"
		}
		return "the Asymptote forwarder needs launchd or systemd to stay running; on this host run Vector yourself with the config from `beacon endpoint asymptote install-pack`"
	default:
		return "the Asymptote forwarder needs launchd or systemd to stay running; on this host run Vector yourself with the config from `beacon endpoint asymptote install-pack`"
	}
}

// Label is the service identifier for status output.
func (m ForwarderManager) Label() string {
	switch m.resolvedKind() {
	case KindSystemd:
		return m.spec().SystemdUnit
	case KindWindowsService:
		if name := m.spec().WindowsService; name != "" {
			return name
		}
	}
	return m.spec().Label
}

// UnitPath returns where the forwarder's service definition lives.
func (m ForwarderManager) UnitPath() (string, error) {
	spec := m.spec()
	switch m.resolvedKind() {
	case KindSystemd:
		if m.UserMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, ".config", "systemd", "user", spec.SystemdUnit), nil
		}
		return filepath.Join("/etc/systemd/system", spec.SystemdUnit), nil
	case KindWindowsService:
		if spec.WindowsService != "" {
			// The definition lives in the registry; this names where, as the collector's backend does.
			return `HKLM\SYSTEM\CurrentControlSet\Services\` + spec.WindowsService, nil
		}
		fallthrough
	default:
		if m.UserMode {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, "Library", "LaunchAgents", spec.Label+".plist"), nil
		}
		return filepath.Join("/Library/LaunchDaemons", spec.Label+".plist"), nil
	}
}

// RemoveUnits deletes the service definition. Missing files are not an error. On Windows the
// definition is the SCM registration, so this stops and deletes the service.
func (m ForwarderManager) RemoveUnits() {
	if m.resolvedKind() == KindWindowsService {
		if m.Supported() {
			_ = removeService(m.spec().WindowsService, m.stopTimeout(), true)
		}
		return
	}
	if path, err := m.UnitPath(); err == nil {
		_ = os.Remove(path)
	}
}

// WriteUnit installs a service definition that runs `vectorBin --config configPath`.
func (m ForwarderManager) WriteUnit(vectorBin, configPath string) (string, error) {
	if !m.Supported() {
		return "", fmt.Errorf("%s", m.UnsupportedReason())
	}
	path, err := m.UnitPath()
	if err != nil {
		return "", err
	}
	if m.resolvedKind() == KindWindowsService {
		return path, createOrUpdateService(m.scmService(vectorBin, configPath))
	}
	if err := ensureDir(path); err != nil {
		return "", err
	}
	if m.resolvedKind() == KindLaunchd {
		if err := ensureLaunchdLogDir(m.UserMode, m.spec().Label); err != nil {
			return "", err
		}
	}
	content, err := m.unitContent(vectorBin, configPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	if m.resolvedKind() == KindSystemd {
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "daemon-reload")...); err != nil {
			return path, systemctlError(out, err, "daemon-reload")
		}
	}
	return path, nil
}

// UnitCurrent reports whether the installed unit is the one WriteUnit would write now. Reconcile
// rewrites one that is not, so a unit an earlier release wrote, such as one logging to /tmp, is
// brought up to date by an upgrade rather than only by a fresh connect.
func (m ForwarderManager) UnitCurrent(vectorBin, configPath string) (bool, error) {
	if m.resolvedKind() == KindWindowsService {
		return serviceCurrent(m.scmService(vectorBin, configPath))
	}
	path, err := m.UnitPath()
	if err != nil {
		return false, err
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	want, err := m.unitContent(vectorBin, configPath)
	if err != nil {
		return false, err
	}
	return string(installed) == want, nil
}

func (m ForwarderManager) unitContent(vectorBin, configPath string) (string, error) {
	spec := m.spec()
	if m.resolvedKind() == KindSystemd {
		return forwarderUnitFile(spec, vectorBin, configPath, m.UserMode), nil
	}
	stdout, stderr, err := launchdLogPaths(m.UserMode, spec.Label)
	if err != nil {
		return "", err
	}
	return forwarderPlist(spec, vectorBin, configPath, stdout, stderr), nil
}

// Load registers and starts the forwarder. An already-loaded launchd job is re-bootstrapped so a
// rewritten config takes effect.
func (m ForwarderManager) Load() error {
	if !m.Supported() {
		return fmt.Errorf("%s", m.UnsupportedReason())
	}
	spec := m.spec()
	switch m.resolvedKind() {
	case KindSystemd:
		unit := spec.SystemdUnit
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "enable", "--now", unit)...); err != nil {
			return systemctlError(out, err, "enable --now "+unit)
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "restart", unit)...); err != nil {
			return systemctlError(out, err, "restart "+unit)
		}
		return nil
	case KindWindowsService:
		// A restart, not a start, so a rewritten config takes effect, then a wait for RUNNING: the
		// SCM reports a start as accepted long before Vector has loaded its config.
		if err := restartService(spec.WindowsService, m.stopTimeout(), true); err != nil {
			return err
		}
		return waitForServiceRunning(spec.WindowsService, forwarderStartTimeout)
	}
	path, err := m.UnitPath()
	if err != nil {
		return err
	}
	domain := serviceDomain(m.UserMode)
	label := spec.Label
	target := domain + "/" + label
	// bootout first so a config rewrite (re-enrollment rotates the key) restarts Vector, then
	// wait for the old instance to be gone: it drains in-flight requests for up to a minute,
	// and a bootstrap issued while it is still registered is silently lost with it. Finally
	// confirm the new instance is actually running rather than trusting the old pid.
	_ = runLaunchctlWithContext(domain, label, "", "bootout", target)
	if !waitForLaunchdJobGone(domain, label) {
		return fmt.Errorf("the previous forwarder did not stop within %s; check `launchctl print %s`", launchdStopTimeout, target)
	}
	if err := loadLaunchdJob(domain, label, path); err != nil {
		return err
	}
	if !waitForLaunchdJobRunning(domain, label) {
		return fmt.Errorf("the forwarder was loaded but has not started within %s; check `launchctl print %s` and Vector's log", launchdStartTimeout, target)
	}
	return nil
}

// Unload stops and deregisters the forwarder. A missing unit is not an error.
func (m ForwarderManager) Unload() error {
	spec := m.spec()
	switch m.resolvedKind() {
	case KindSystemd:
		if !systemdIsInit() {
			return nil
		}
		unit := spec.SystemdUnit
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "stop", unit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "stop "+unit)
		}
		if out, err := runSystemctlCommand(systemctlArgs(m.UserMode, "disable", unit)...); err != nil && !systemdUnitMissing(out) {
			return systemctlError(out, err, "disable "+unit)
		}
		return nil
	case KindLaunchd:
		if runtime.GOOS != "darwin" {
			return nil
		}
		return bootoutLaunchdJob(serviceDomain(m.UserMode), spec.Label)
	case KindWindowsService:
		// Stops, waits for Vector's process to exit so vector.exe can be replaced, and deletes the
		// registration. A missing service is success.
		if !m.Supported() {
			return nil
		}
		return removeService(spec.WindowsService, m.stopTimeout(), true)
	default:
		return nil
	}
}

// Status reports whether the forwarder is loaded and running.
func (m ForwarderManager) Status() Status {
	kind := m.resolvedKind()
	if !m.Supported() {
		return Status{Label: m.Label(), Kind: string(kind), Message: m.UnsupportedReason()}
	}
	spec := m.spec()
	if kind == KindWindowsService {
		status := queryService(spec.WindowsService)
		if !status.Loaded && !status.Running && status.Message == "service not installed" {
			status.Message = "forwarder not installed"
		}
		return status
	}
	if kind == KindSystemd {
		status := Status{Label: spec.SystemdUnit, Kind: string(KindSystemd)}
		enabledOut, _ := runSystemctlCommand(systemctlArgs(m.UserMode, "is-enabled", spec.SystemdUnit)...)
		switch strings.TrimSpace(enabledOut) {
		case "enabled", "enabled-runtime", "static":
			status.Loaded = true
		}
		activeOut, _ := runSystemctlCommand(systemctlArgs(m.UserMode, "is-active", spec.SystemdUnit)...)
		status.Running = strings.TrimSpace(activeOut) == "active"
		if status.Running {
			status.Loaded = true
		}
		if !status.Loaded && !status.Running {
			status.Message = "forwarder not installed"
		}
		return status
	}
	status := Status{Label: spec.Label, Kind: string(KindLaunchd)}
	out, err := runLaunchctlCommand("print", serviceDomain(m.UserMode)+"/"+spec.Label)
	if err != nil {
		status.Message = strings.TrimSpace(out)
		return status
	}
	status.Loaded = true
	status.Running = strings.Contains(out, "state = running") || strings.Contains(out, "pid =")
	return status
}

// forwarderUnitFile renders the systemd unit. Vector reads its own config; the unit adds only the
// spec's environment.
func forwarderUnitFile(spec ForwarderSpec, vectorBin, configPath string, userMode bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", spec.Description)
	if spec.Documentation != "" {
		fmt.Fprintf(&b, "Documentation=%s\n", spec.Documentation)
	}
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	args := []string{systemdArg(vectorBin), "--config", systemdArg(configPath)}
	for _, arg := range spec.Args {
		args = append(args, systemdArg(arg))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	for _, entry := range spec.Env {
		fmt.Fprintf(&b, "Environment=%s\n", systemdArg(entry))
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	if !userMode {
		// System mode reads /var/log/beacon-agent, which root owns.
		b.WriteString("User=root\n")
	}
	b.WriteString("\n[Install]\n")
	if userMode {
		b.WriteString("WantedBy=default.target\n")
	} else {
		b.WriteString("WantedBy=multi-user.target\n")
	}
	return b.String()
}

// forwarderPlist renders the launchd job: resident, restarted by launchd, started at load.
func forwarderPlist(spec ForwarderSpec, vectorBin, configPath, stdoutPath, stderrPath string) string {
	var args strings.Builder
	for _, arg := range append([]string{vectorBin, "--config", configPath}, spec.Args...) {
		fmt.Fprintf(&args, "    <string>%s</string>\n", xmlEscape(arg))
	}
	var env string
	if len(spec.Env) > 0 {
		var b strings.Builder
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, entry := range spec.Env {
			name, value, _ := strings.Cut(entry, "=")
			fmt.Fprintf(&b, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(name), xmlEscape(value))
		}
		b.WriteString("  </dict>\n")
		env = b.String()
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
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(spec.Label), args.String(), env, xmlEscape(stdoutPath), xmlEscape(stderrPath))
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(s)
}
