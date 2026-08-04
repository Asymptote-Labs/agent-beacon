// Package service manages the long-running collector as an operating-system service.
//
// Manager keeps its value-struct shape so existing call sites are unaffected, but dispatches
// to a platform backend chosen at construction time:
//
//	launchd     macOS, the original and still the default there
//	systemd     Linux with systemd as PID 1 (servers, workstations, VMs)
//	supervised  anything else -- a detached child process with a pidfile, which is what
//	            containers and CI actually want, and what `beacon ci` has always done
//
// Auto-detection covers the common cases so one `beacon endpoint install` works everywhere;
// Kind can be set explicitly when detection would guess wrong.
package service

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

const (
	SystemLabel = "com.beacon.endpoint.collector"
	UserLabel   = "com.beacon.endpoint.collector.user"

	// SystemdSystemUnit and SystemdUserUnit are the Linux unit names. Systemd convention is
	// short lowercase names rather than launchd's reverse-DNS labels.
	SystemdSystemUnit = "beacon-collector.service"
	SystemdUserUnit   = "beacon-collector.service"
)

// Kind identifies which service manager backs a Manager.
type Kind string

const (
	// KindAuto asks for detection. This is the zero value, so an unset Manager behaves
	// sensibly on every platform.
	KindAuto Kind = ""
	// KindLaunchd is macOS launchd.
	KindLaunchd Kind = "launchd"
	// KindSystemd is Linux systemd.
	KindSystemd Kind = "systemd"
	// KindSupervised is a detached child process tracked by a pidfile. No OS service
	// manager is involved, so it works in containers, CI, and on systems without systemd.
	KindSupervised Kind = "none"
)

// ParseKind validates a user-supplied service kind.
func ParseKind(s string) (Kind, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(s))) {
	case KindAuto, "auto":
		return KindAuto, nil
	case KindLaunchd:
		return KindLaunchd, nil
	case KindSystemd:
		return KindSystemd, nil
	case KindSupervised, "supervised":
		return KindSupervised, nil
	default:
		return "", fmt.Errorf("unsupported service kind %q; want auto, launchd, systemd, or none", s)
	}
}

// EnableLingerIfNeeded makes a systemd --user unit survive logout, where that applies.
//
// Returns ("", ) for every other case -- system mode, and every backend but systemd -- so the
// caller records nothing rather than reporting a step that was never relevant. launchd needs no
// equivalent: its gui/<uid> domain persists for the login session by itself.
func (m Manager) EnableLingerIfNeeded() (bool, string) {
	if !m.UserMode || m.resolvedKind() != KindSystemd {
		return false, ""
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return false, "could not determine the current user, so logout persistence is unverified"
	}
	if LingerEnabled(u.Username) {
		return true, "linger already enabled for " + u.Username
	}
	return EnableLinger(u.Username)
}

// ServiceNoun names what this backend installs, for user-facing messages.
//
// Shared rather than duplicated: the install planner and the doctor fix planner both describe
// the same artifact, and the codebase already had several independently hardcoded copies of
// macOS-specific wording that had to be hunted down when systemd support landed.
func (k Kind) ServiceNoun() string {
	switch k {
	case KindLaunchd:
		return "launchd service definition"
	case KindSystemd:
		return "systemd unit definition"
	case KindSupervised:
		return "supervised collector state (no service manager on this host)"
	default:
		return "collector service definition"
	}
}

// Manager controls the collector service.
type Manager struct {
	UserMode bool
	// Kind selects the backend. The zero value auto-detects.
	Kind Kind
}

// Status describes the service as the OS currently sees it.
type Status struct {
	Label   string `json:"label"`
	Loaded  bool   `json:"loaded"`
	Running bool   `json:"running"`
	Message string `json:"message,omitempty"`
	// Kind records which backend produced this status, so `status --json` and doctor output
	// are interpretable on a mixed fleet.
	Kind string `json:"kind,omitempty"`
}

// backend is the per-platform implementation. Methods take userMode rather than closing over
// it so a backend value stays stateless and cheap to construct.
type backend interface {
	kind() Kind
	// label is the service identifier: a launchd label, a systemd unit name, or a pidfile
	// basename.
	label(userMode bool) string
	// unitPath is where the service definition lives on disk. Supervised mode has no unit
	// file and reports its pidfile instead, so callers get something meaningful to show.
	unitPath(userMode bool) (string, error)
	// writeUnit renders and installs the service definition.
	writeUnit(userMode bool, program, configPath string) (string, error)
	load(userMode bool) error
	unload(userMode bool) error
	restart(userMode bool) error
	status(userMode bool) Status
	// available reports whether this backend can actually manage services here.
	available() bool
	// unsupportedReason explains why not, for actionable errors.
	unsupportedReason() string
}

// DetectKind picks a backend for this host.
//
// Ordering is deliberate: a real service manager is preferred when present, because a
// persistent endpoint install should survive logout and reboot. Supervised mode is the
// fallback rather than the default so containers work without configuration while a
// workstation still gets a proper service.
func DetectKind() Kind {
	switch runtime.GOOS {
	case "darwin":
		return KindLaunchd
	case "linux":
		if systemdIsInit() {
			return KindSystemd
		}
		return KindSupervised
	default:
		return KindSupervised
	}
}

func (m Manager) resolvedKind() Kind {
	if m.Kind != KindAuto {
		return m.Kind
	}
	return DetectKind()
}

func (m Manager) backend() backend {
	switch m.resolvedKind() {
	case KindLaunchd:
		return launchdBackend{}
	case KindSystemd:
		return systemdBackend{}
	default:
		return supervisedBackend{}
	}
}

// ResolvedKind reports the backend in use, for status output and diagnostics.
func (m Manager) ResolvedKind() Kind { return m.resolvedKind() }

// Available reports whether the resolved backend can manage services on this host.
func (m Manager) Available() bool { return m.backend().available() }

// UnsupportedReason explains why Available is false.
func (m Manager) UnsupportedReason() string { return m.backend().unsupportedReason() }

func (m Manager) Label() string { return m.backend().label(m.UserMode) }

// UnitPath is the on-disk service definition: a launchd plist, a systemd unit, or (in
// supervised mode, which has no unit file) the pidfile.
func (m Manager) UnitPath() (string, error) { return m.backend().unitPath(m.UserMode) }

// WriteUnit installs the service definition and returns its path.
func (m Manager) WriteUnit(program, configPath string) (string, error) {
	b := m.backend()
	if !b.available() {
		return "", fmt.Errorf("%s", b.unsupportedReason())
	}
	return b.writeUnit(m.UserMode, program, configPath)
}

// Load registers and starts the service.
func (m Manager) Load() error {
	b := m.backend()
	if !b.available() {
		return fmt.Errorf("%s", b.unsupportedReason())
	}
	return b.load(m.UserMode)
}

// Unload stops and deregisters the service. Missing services are not an error, so this is
// safe to call during uninstall and repair.
func (m Manager) Unload() error {
	b := m.backend()
	if !b.available() {
		// Nothing to unload if the backend cannot run here; uninstall should still proceed.
		return nil
	}
	return b.unload(m.UserMode)
}

// Restart restarts the service, starting it if it is not loaded.
func (m Manager) Restart() error {
	b := m.backend()
	if !b.available() {
		return fmt.Errorf("%s", b.unsupportedReason())
	}
	return b.restart(m.UserMode)
}

// Status reports the current service state.
func (m Manager) Status() Status {
	b := m.backend()
	if !b.available() {
		return Status{Label: b.label(m.UserMode), Kind: string(b.kind()), Message: b.unsupportedReason()}
	}
	s := b.status(m.UserMode)
	s.Kind = string(b.kind())
	return s
}

// stateDir is where service-owned runtime state (pidfiles) lives. Derived from the endpoint
// config so it tracks the single SystemBaseDir definition rather than duplicating it.
func stateDir(userMode bool) string { return endpointconfig.BaseDir(userMode) }

// ensureDir creates a directory for a service definition, tolerating an existing one.
func ensureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}
