//go:build windows

package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceStopTimeout bounds how long unload waits for a graceful stop before deleting anyway.
//
// The collector flushes on shutdown, so cutting this short loses whatever the exporter had
// buffered -- the same reason the supervised backend signals before it kills.
const serviceStopTimeout = 20 * time.Second

// withManager opens the SCM and hands it to fn.
//
// Every entry point needs it, and every one of them fails the same way without administrator
// rights, so the "run elevated" advice is attached once here rather than at each call site.
func withManager(fn func(*mgr.Mgr) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open the Windows Service Control Manager (this needs an elevated shell): %w", err)
	}
	defer m.Disconnect()
	return fn(m)
}

// createOrUpdateService registers the collector, or points an existing registration at the new
// binary and config.
//
// Updating in place rather than delete-and-recreate: a delete only takes effect once every handle
// to the service closes, so a recreate immediately afterwards can fail with "marked for deletion"
// on a machine where services.msc happens to be open. Reconfiguring has no such window, and an
// upgrade that changes the binary path is the common case.
func createOrUpdateService(program, configPath string) error {
	return withManager(func(m *mgr.Mgr) error {
		cfg := mgr.Config{
			DisplayName: WindowsServiceDisplayName,
			Description: "Collects local AI agent telemetry and writes the Beacon endpoint log.",
			// Automatic is the equivalent of launchd's RunAtLoad and systemd's
			// WantedBy=multi-user.target: the endpoint must come back after a reboot without
			// anyone logging in.
			StartType: mgr.StartAutomatic,
			// The collector binds loopback and writes a local log. Delayed start keeps it out of
			// the boot-time contention that would otherwise have it restarting while the network
			// stack settles -- the same reasoning as the systemd unit's After=network.target.
			DelayedAutoStart: true,
		}

		existing, err := m.OpenService(WindowsServiceName)
		if err == nil {
			defer existing.Close()
			current, cerr := existing.Config()
			if cerr != nil {
				return fmt.Errorf("read the existing %s service configuration: %w", WindowsServiceName, cerr)
			}
			current.DisplayName = cfg.DisplayName
			current.Description = cfg.Description
			current.StartType = cfg.StartType
			current.DelayedAutoStart = cfg.DelayedAutoStart
			current.BinaryPathName = serviceBinaryPath(program, configPath)
			if err := existing.UpdateConfig(current); err != nil {
				return fmt.Errorf("update the %s service: %w", WindowsServiceName, err)
			}
			return setRecoveryActions(existing)
		}

		created, err := m.CreateService(WindowsServiceName, program, cfg, "--config", configPath)
		if err != nil {
			return fmt.Errorf("register the %s service: %w", WindowsServiceName, err)
		}
		defer created.Close()
		return setRecoveryActions(created)
	})
}

// setRecoveryActions is the KeepAlive / Restart=always equivalent.
//
// Without it a collector that crashes stays down until the next reboot, which is the difference
// between a service manager and a one-shot launcher -- and the whole reason to prefer the SCM over
// the supervised backend. Three escalating restarts rather than infinite immediate ones: a
// collector that cannot start at all should stop thrashing and leave a visible stopped service,
// not loop forever writing the same error.
func setRecoveryActions(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	// The reset period returns the failure count to zero after a day of health, so a service that
	// dies once a week does not eventually exhaust its restarts.
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("set restart-on-failure for %s: %w", WindowsServiceName, err)
	}
	return nil
}

// serviceBinaryPath renders the ImagePath for an update.
//
// CreateService takes the executable and its arguments separately and quotes them itself, but
// UpdateConfig takes one string, so the quoting is ours to get right. An unquoted path breaks at
// the first space -- and the default install location is under %ProgramFiles%, which contains one.
//
// Escaped exactly the way CreateService escapes, by calling the same function it calls. Go's %q is
// the wrong tool and fails in a way that only shows up on the upgrade path: it doubles every
// backslash, so `C:\Program Files\Beacon\bin\beacon-otelcol.exe` is written to the registry as
// `C:\\Program Files\\...`, which the SCM does not unescape. A fresh install would work, the
// upgrade would report success, and the service would fail to start after the next reboot with a
// file-not-found the operator has no reason to connect to an upgrade. Sharing the escaper also
// means create and update cannot drift apart.
func serviceBinaryPath(program, configPath string) string {
	parts := make([]string, 0, 3)
	for _, arg := range []string{program, "--config", configPath} {
		parts = append(parts, windows.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func startService() error {
	return withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(WindowsServiceName)
		if err != nil {
			return fmt.Errorf("open the %s service (was it installed?): %w", WindowsServiceName, err)
		}
		defer s.Close()

		status, err := s.Query()
		if err == nil && status.State == svc.Running {
			return nil // already running; install and repair both call this speculatively
		}
		if err := s.Start(); err != nil {
			// An already-running service is not a failure, and the SCM reports it as an error.
			if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
				return nil
			}
			return fmt.Errorf("start the %s service: %w", WindowsServiceName, err)
		}
		return nil
	})
}

// removeService stops and deletes the registration, treating an absent service as success.
func removeService() error {
	return withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(WindowsServiceName)
		if err != nil {
			// Nothing registered. Uninstall and repair both call this speculatively.
			return nil
		}
		defer s.Close()

		if err := stopAndWait(s); err != nil {
			return err
		}
		if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return fmt.Errorf("delete the %s service: %w", WindowsServiceName, err)
		}
		return nil
	})
}

// stopAndWait asks the service to stop and waits for it, so a delete does not race a running
// process. A service that will not stop is reported rather than force-killed: the SCM has no
// equivalent of SIGKILL for a service, and pretending otherwise would hide a wedged collector.
func stopAndWait(s *mgr.Service) error {
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query the %s service: %w", WindowsServiceName, err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		// Already stopped between the query and the control request; that is the outcome we want.
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return fmt.Errorf("stop the %s service: %w", WindowsServiceName, err)
	}

	deadline := time.Now().Add(serviceStopTimeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return fmt.Errorf("query the %s service while stopping: %w", WindowsServiceName, err)
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the %s service did not stop within %s", WindowsServiceName, serviceStopTimeout)
}

func restartService() error {
	if err := withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(WindowsServiceName)
		if err != nil {
			// Not installed. Installing it is the right recovery, and mirrors the systemd
			// backend's restart-falls-back-to-load behavior.
			return errServiceNotInstalled
		}
		defer s.Close()
		return stopAndWait(s)
	}); err != nil {
		if errors.Is(err, errServiceNotInstalled) {
			return startService()
		}
		return err
	}
	return startService()
}

// errServiceNotInstalled distinguishes "there is nothing to restart" from a real failure, so
// restart can fall back to starting rather than reporting an error for a recoverable state.
var errServiceNotInstalled = errors.New("service is not installed")

func queryService() Status {
	status := Status{Label: WindowsServiceName}
	err := withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(WindowsServiceName)
		if err != nil {
			status.Message = "service not installed"
			return nil
		}
		defer s.Close()

		// Loaded means registered and intended to run, matching what the other backends report
		// for an enabled unit or a bootstrapped job.
		cfg, cerr := s.Config()
		if cerr == nil {
			status.Loaded = cfg.StartType == mgr.StartAutomatic || cfg.StartType == mgr.StartManual
		}
		st, qerr := s.Query()
		if qerr != nil {
			status.Message = "could not query the service: " + qerr.Error()
			return nil
		}
		status.Running = st.State == svc.Running
		if status.Running {
			// A running service is registered regardless of how it was configured to start.
			status.Loaded = true
		} else {
			status.Message = "service state: " + serviceStateName(st.State)
		}
		return nil
	})
	if err != nil {
		status.Message = firstLineOf(err.Error())
	}
	return status
}

// serviceStateName renders an SCM state for a human. The raw numbers appear in `sc query` output
// and mean nothing to most readers.
func serviceStateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "resuming"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown (%d)", state)
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
