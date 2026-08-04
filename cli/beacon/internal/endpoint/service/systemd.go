package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// systemdBackend manages the collector via systemd.
//
// Mapping from the launchd behavior it replaces:
//
//	RunAtLoad=true   -> WantedBy=multi-user.target plus `systemctl enable --now`
//	KeepAlive=true   -> Restart=always with a small RestartSec
//	StandardOutPath  -> the journal, so no /tmp files to manage or rotate
//	gui/<uid> domain -> `systemctl --user`, which additionally needs linger enabled to
//	                    survive logout (see EnableLinger)
type systemdBackend struct{}

var runSystemctlCommand = func(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var runLoginctlCommand = func(args ...string) (string, error) {
	cmd := exec.Command("loginctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// systemdIsInit reports whether systemd is PID 1.
//
// Checked by reading /proc/1/comm rather than by probing for the systemctl binary: many
// container images ship systemctl while running some other init, and in that case every
// systemctl call fails with "System has not been booted with systemd as init system". Getting
// this wrong would make install appear to succeed and then never start anything.
func systemdIsInit() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/1/comm")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "systemd"
}

func (systemdBackend) kind() Kind { return KindSystemd }

func (systemdBackend) available() bool { return systemdIsInit() }

func (systemdBackend) unsupportedReason() string {
	if runtime.GOOS != "linux" {
		return "systemd service management is available only on Linux"
	}
	return "systemd is not PID 1 on this host; use --service=none for a supervised collector process"
}

func (systemdBackend) label(userMode bool) string {
	if userMode {
		return SystemdUserUnit
	}
	return SystemdSystemUnit
}

func (b systemdBackend) unitPath(userMode bool) (string, error) {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "systemd", "user", SystemdUserUnit), nil
	}
	return filepath.Join("/etc/systemd/system", SystemdSystemUnit), nil
}

// systemctlArgs prefixes --user for user-mode units so one code path serves both scopes.
func systemctlArgs(userMode bool, args ...string) []string {
	if userMode {
		return append([]string{"--user"}, args...)
	}
	return args
}

func (b systemdBackend) writeUnit(userMode bool, program, configPath string) (string, error) {
	path, err := b.unitPath(userMode)
	if err != nil {
		return "", err
	}
	if err := ensureDir(path); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(unitFile(program, configPath, userMode)), 0o644); err != nil {
		return "", err
	}
	// systemd caches unit files; without a reload it will keep using the previous contents,
	// which makes a repair look successful while changing nothing.
	if out, err := runSystemctlCommand(systemctlArgs(userMode, "daemon-reload")...); err != nil {
		return path, systemctlError(out, err, "daemon-reload")
	}
	return path, nil
}

func (b systemdBackend) load(userMode bool) error {
	unit := b.label(userMode)
	if out, err := runSystemctlCommand(systemctlArgs(userMode, "enable", "--now", unit)...); err != nil {
		return systemctlError(out, err, "enable --now "+unit)
	}
	return nil
}

func (b systemdBackend) unload(userMode bool) error {
	unit := b.label(userMode)
	// Stop before disable so a running collector is actually terminated, and tolerate an
	// absent unit: uninstall and repair both call this speculatively.
	if out, err := runSystemctlCommand(systemctlArgs(userMode, "stop", unit)...); err != nil {
		if !systemdUnitMissing(out) {
			return systemctlError(out, err, "stop "+unit)
		}
	}
	if out, err := runSystemctlCommand(systemctlArgs(userMode, "disable", unit)...); err != nil {
		if !systemdUnitMissing(out) {
			return systemctlError(out, err, "disable "+unit)
		}
	}
	return nil
}

func (b systemdBackend) restart(userMode bool) error {
	unit := b.label(userMode)
	out, err := runSystemctlCommand(systemctlArgs(userMode, "restart", unit)...)
	if err == nil {
		return nil
	}
	// A unit that was never enabled cannot be restarted; installing it is the right recovery
	// and mirrors the launchd backend's kickstart-then-load fallback.
	if systemdUnitMissing(out) {
		return b.load(userMode)
	}
	return systemctlError(out, err, "restart "+unit)
}

func (b systemdBackend) status(userMode bool) Status {
	unit := b.label(userMode)
	status := Status{Label: unit}

	// is-enabled exits non-zero for a disabled unit, which is a state rather than a failure,
	// so the output is what matters and the error is ignored.
	enabledOut, _ := runSystemctlCommand(systemctlArgs(userMode, "is-enabled", unit)...)
	enabled := strings.TrimSpace(enabledOut)
	switch enabled {
	case "enabled", "enabled-runtime", "static", "indirect", "linked", "linked-runtime":
		status.Loaded = true
	}

	activeOut, _ := runSystemctlCommand(systemctlArgs(userMode, "is-active", unit)...)
	active := strings.TrimSpace(activeOut)
	status.Running = active == "active"
	if status.Running {
		// A running unit is loaded regardless of whether it is enabled at boot.
		status.Loaded = true
	}

	if !status.Loaded && !status.Running {
		status.Message = strings.TrimSpace(enabled + " " + active)
		if status.Message == "" {
			status.Message = "unit not installed"
		}
	}
	return status
}

// systemdUnitMissing recognizes the several ways systemd reports an absent unit.
func systemdUnitMissing(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not loaded") ||
		strings.Contains(l, "could not be found") ||
		strings.Contains(l, "no such file or directory") ||
		strings.Contains(l, "does not exist")
}

func systemctlError(out string, err error, what string) error {
	text := strings.TrimSpace(out)
	guidance := ""
	if strings.Contains(strings.ToLower(text), "interactive authentication required") {
		guidance = "\nsystemd requires privileges for system units; rerun with sudo, or use --user for a per-user service."
	}
	if text == "" {
		return fmt.Errorf("systemctl %s failed: %w%s", what, err, guidance)
	}
	return fmt.Errorf("systemctl %s failed: %s: %w%s", what, text, err, guidance)
}

// EnableLinger allows a --user unit to keep running after the user logs out.
//
// This is the piece with no macOS counterpart: launchd's gui/<uid> domain persists for the
// login session, whereas a systemd user manager is torn down at logout unless linger is set.
// Without it a user-mode install silently stops collecting the moment the user logs out.
// Best-effort, since it needs privileges that a plain user may not have.
func EnableLinger(username string) (bool, string) {
	if !systemdIsInit() {
		return false, "systemd is not PID 1; linger does not apply"
	}
	if username == "" {
		return false, "no username provided"
	}
	out, err := runLoginctlCommand("enable-linger", username)
	if err != nil {
		// loginctl does not always print anything on failure, and an empty detail here is
		// indistinguishable from "linger does not apply" -- which is what the caller uses an empty
		// detail to mean. The error is folded in so the outcome is always attributable.
		if detail := strings.TrimSpace(out); detail != "" {
			return false, detail
		}
		return false, "loginctl enable-linger " + username + " failed: " + err.Error()
	}
	// The success path must say so. It previously returned an empty detail, which the install path
	// reads as "not applicable" -- so the one outcome worth recording, linger actually being
	// enabled, was the one the manifest silently dropped.
	return true, "linger enabled for " + username
}

// LingerEnabled reports whether linger is on for a user, so doctor can flag the gap.
func LingerEnabled(username string) bool {
	if !systemdIsInit() || username == "" {
		return false
	}
	out, err := runLoginctlCommand("show-user", username, "--property=Linger", "--value")
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(out), "yes")
}

// systemdArg renders one value as a single ExecStart argument.
//
// systemd splits ExecStart on whitespace, so an unquoted path containing a space becomes two
// arguments and the unit fails with a confusing "No such file or directory". That is not exotic:
// a user-mode install lives under the user's home directory, and `--collector` takes an arbitrary
// path from the caller.
//
// Double quotes are systemd's own quoting, inside which backslash escapes apply -- so both the
// quote and the backslash have to be escaped. `%` is separate: it introduces a unit specifier
// anywhere on the line, quoted or not, and is escaped by doubling.
func systemdArg(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "%", "%%")
	return `"` + value + `"`
}

// unitFile renders the collector unit.
func unitFile(program, configPath string, userMode bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Beacon endpoint collector\n")
	b.WriteString("Documentation=https://docs.asymptotelabs.ai/cli/endpoint\n")
	// The collector only binds loopback and writes a local log, but ordering after the
	// network target avoids a restart storm during early boot.
	b.WriteString("After=network.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s --config %s\n", systemdArg(program), systemdArg(configPath))
	// KeepAlive equivalent.
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	// stdout/stderr go to the journal, replacing launchd's /tmp/<label>.out files.
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	if !userMode {
		// System mode runs as root because the collector writes /var/log/beacon-agent, which
		// hooks running as the console user also append to. Hardening that is deliberately
		// left for the packaging milestone, where the log directory ownership story is
		// decided.
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
