//go:build windows

package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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

// createOrUpdateService registers a service, or points an existing registration at the new binary,
// arguments and environment.
//
// Updating in place rather than delete-and-recreate: a delete only takes effect once every handle
// to the service closes, so a recreate immediately afterwards can fail with "marked for deletion"
// on a machine where services.msc happens to be open. Reconfiguring has no such window, and an
// upgrade that changes the binary path is the common case.
func createOrUpdateService(spec scmService) error {
	return withManager(func(m *mgr.Mgr) error {
		cfg := mgr.Config{
			DisplayName: spec.DisplayName,
			Description: spec.Description,
			// Automatic is the equivalent of launchd's RunAtLoad and systemd's
			// WantedBy=multi-user.target: the endpoint must come back after a reboot without
			// anyone logging in.
			StartType: mgr.StartAutomatic,
			// The collector binds loopback and writes a local log. Delayed start keeps it out of
			// the boot-time contention that would otherwise have it restarting while the network
			// stack settles -- the same reasoning as the systemd unit's After=network.target.
			DelayedAutoStart: true,
		}

		existing, err := m.OpenService(spec.Name)
		if err == nil {
			defer existing.Close()
			current, cerr := existing.Config()
			if cerr != nil {
				return fmt.Errorf("read the existing %s service configuration: %w", spec.Name, cerr)
			}
			current.DisplayName = cfg.DisplayName
			current.Description = cfg.Description
			current.StartType = cfg.StartType
			current.DelayedAutoStart = cfg.DelayedAutoStart
			current.BinaryPathName = serviceBinaryPath(spec.Program, spec.Args...)
			if err := existing.UpdateConfig(current); err != nil {
				return fmt.Errorf("update the %s service: %w", spec.Name, err)
			}
			if err := setServiceEnvironment(spec); err != nil {
				return err
			}
			return setRecoveryActions(existing, spec.Name)
		}

		created, err := m.CreateService(spec.Name, spec.Program, cfg, spec.Args...)
		if err != nil {
			return fmt.Errorf("register the %s service: %w", spec.Name, err)
		}
		defer created.Close()
		if err := setServiceEnvironment(spec); err != nil {
			return err
		}
		return setRecoveryActions(created, spec.Name)
	})
}

// setServiceEnvironment writes the variables the SCM adds to the service process's environment:
// the Environment value under the service's key, one NAME=value string per entry. No variables
// removes the value, so a registration that once carried some does not keep them.
func setServiceEnvironment(spec scmService) error {
	if !spec.OwnsEnv {
		return nil
	}
	name, env := spec.Name, spec.Env
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, serviceRegistryKey(name), registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open the %s service's registry key: %w", name, err)
	}
	defer key.Close()
	if len(env) == 0 {
		if err := key.DeleteValue("Environment"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("clear the %s service's environment: %w", name, err)
		}
		return nil
	}
	if err := key.SetStringsValue("Environment", env); err != nil {
		return fmt.Errorf("set the %s service's environment: %w", name, err)
	}
	return nil
}

// serviceEnvironment reads back what setServiceEnvironment wrote.
func serviceEnvironment(name string) ([]string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, serviceRegistryKey(name), registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer key.Close()
	env, _, err := key.GetStringsValue("Environment")
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	return env, err
}

func serviceRegistryKey(name string) string {
	return `SYSTEM\CurrentControlSet\Services\` + name
}

// serviceCurrent reports whether the registered service runs what spec describes, so a reconcile
// rewrites only a registration that changed.
func serviceCurrent(spec scmService) (bool, error) {
	var current bool
	err := withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(spec.Name)
		if err != nil {
			return err
		}
		defer s.Close()
		cfg, err := s.Config()
		if err != nil {
			return err
		}
		env, err := serviceEnvironment(spec.Name)
		if err != nil {
			return err
		}
		current = strings.EqualFold(cfg.BinaryPathName, serviceBinaryPath(spec.Program, spec.Args...)) &&
			cfg.StartType == mgr.StartAutomatic && slices.Equal(env, spec.Env)
		return nil
	})
	return current, err
}

// setRecoveryActions is the KeepAlive / Restart=always equivalent.
//
// Without it a collector that crashes stays down until the next reboot, which is the difference
// between a service manager and a one-shot launcher -- and the whole reason to prefer the SCM over
// the supervised backend. Three escalating restarts rather than infinite immediate ones: a
// collector that cannot start at all should stop thrashing and leave a visible stopped service,
// not loop forever writing the same error.
func setRecoveryActions(s *mgr.Service, name string) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	// The reset period returns the failure count to zero after a day of health, so a service that
	// dies once a week does not eventually exhaust its restarts.
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("set restart-on-failure for %s: %w", name, err)
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
func serviceBinaryPath(program string, args ...string) string {
	parts := make([]string, 0, 1+len(args))
	for _, arg := range append([]string{program}, args...) {
		parts = append(parts, windows.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func startService(name string) error {
	return withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(name)
		if err != nil {
			return fmt.Errorf("open the %s service (was it installed?): %w", name, err)
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
			return fmt.Errorf("start the %s service: %w", name, err)
		}
		return nil
	})
}

// waitForServiceRunning waits until the service reports running. A service that stops instead, or
// is still starting when the time is up, is an error naming its state: a process that hangs in
// START_PENDING cannot be stopped and never triggers its recovery actions, so it must not be
// reported as started.
func waitForServiceRunning(name string, timeout time.Duration) error {
	return withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(name)
		if err != nil {
			return fmt.Errorf("open the %s service: %w", name, err)
		}
		defer s.Close()
		deadline := time.Now().Add(timeout)
		for {
			status, err := s.Query()
			if err != nil {
				return fmt.Errorf("query the %s service: %w", name, err)
			}
			switch status.State {
			case svc.Running:
				return nil
			case svc.Stopped:
				return fmt.Errorf("the %s service stopped while starting (exit code %d)", name, status.Win32ExitCode)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("the %s service is still %s after %s", name, serviceStateName(status.State), timeout)
			}
			time.Sleep(300 * time.Millisecond)
		}
	})
}

// removeService stops and deletes the registration, treating an absent service as success.
//
// terminateHung ends the service's process when it will not stop, and deletes the registration
// anyway. The collector never sets it: a wedged collector is reported, not hidden. A forwarder does,
// because Vector hangs in START_PENDING when it fails to start, and a service in that state refuses
// the stop request outright, which would otherwise leave it registered, running and holding
// vector.exe. Ending Vector loses nothing: its read position is saved only for what Azure accepted.
func removeService(name string, stopTimeout time.Duration, terminateHung bool) error {
	return withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(name)
		if err != nil {
			// Nothing registered. Uninstall and repair both call this speculatively.
			return nil
		}
		defer s.Close()

		if err := stopAndWait(s, name, stopTimeout, terminateHung); err != nil {
			return err
		}
		if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return fmt.Errorf("delete the %s service: %w", name, err)
		}
		return nil
	})
}

// stopAndWait asks the service to stop and waits until its process has exited, so a delete does
// not race a running process and a caller that then removes or replaces the binary finds it free.
// A service that will not stop is reported rather than force-killed: the SCM has no equivalent of
// SIGKILL for a service, and pretending otherwise would hide a wedged collector. terminateHung is
// the exception, for a forwarder (see removeService): its process is ended instead.
//
// SERVICE_STOPPED alone is not enough. The collector reports it from inside its service handler,
// and the process still has to return from svc.Run and exit before Windows releases the image. An
// MSI uninstall or upgrade that reaches beacon-otelcol.exe in that window cannot remove it,
// schedules the delete for the next boot, and -- under msiexec /qn without /norestart, which is how
// fleet tooling runs it -- restarts the machine on the spot.
func stopAndWait(s *mgr.Service, name string, timeout time.Duration, terminateHung bool) error {
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query the %s service: %w", name, err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	// Opened before the stop request, so the handle is to this service's process and cannot become
	// whatever reuses the PID after it exits. Best effort: without a handle the wait below falls
	// back to the service state alone, which is what it always was.
	process := openServiceProcess(status.ProcessId, terminateHung)
	if process != 0 {
		defer windows.CloseHandle(process)
	}
	deadline := time.Now().Add(timeout)
	// giveUp is every way the stop can fail: reported as it is, or, for a forwarder, by ending the
	// process. That covers a service that refuses the stop (Vector stuck in START_PENDING does), one
	// that never reports STOPPED, and one that reports it while its process lives on.
	giveUp := func(stopErr error) error {
		if !terminateHung || process == 0 {
			return stopErr
		}
		if err := windows.TerminateProcess(process, 1); err != nil {
			return errors.Join(stopErr, fmt.Errorf("end the %s service's process: %w", name, err))
		}
		if err := waitForServiceProcess(process, name, time.Now().Add(timeout), timeout); err != nil {
			return errors.Join(stopErr, err)
		}
		// The SCM learns of the exit on its own, a moment later. Until it has, the service still
		// reads as pending, and a start issued now is refused as already running.
		settle := time.Now().Add(timeout)
		for time.Now().Before(settle) {
			if status, err := s.Query(); err == nil && status.State == svc.Stopped {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return errors.Join(stopErr, fmt.Errorf("the %s service's process was ended but the service never reported stopped", name))
	}

	if _, err := s.Control(svc.Stop); err != nil {
		// Already stopped between the query and the control request; that is the outcome we want,
		// once its process is gone.
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			if err := waitForServiceProcess(process, name, deadline, timeout); err != nil {
				return giveUp(err)
			}
			return nil
		}
		return giveUp(fmt.Errorf("stop the %s service: %w", name, err))
	}

	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return giveUp(fmt.Errorf("query the %s service while stopping: %w", name, err))
		}
		if status.State == svc.Stopped {
			if err := waitForServiceProcess(process, name, deadline, timeout); err != nil {
				return giveUp(err)
			}
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return giveUp(fmt.Errorf("the %s service did not stop within %s", name, timeout))
}

// openServiceProcess returns a handle that can wait on the service's process, and end it when
// terminate is set, or 0 when there is no process to wait on or it cannot be opened.
func openServiceProcess(pid uint32, terminate bool) windows.Handle {
	if pid == 0 {
		return 0
	}
	if terminate {
		if h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid); err == nil {
			return h
		}
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0
	}
	return h
}

// waitForServiceProcess waits, until deadline, for the process behind a stopped service to exit.
func waitForServiceProcess(process windows.Handle, name string, deadline time.Time, timeout time.Duration) error {
	if process == 0 {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	event, err := windows.WaitForSingleObject(process, uint32(remaining.Milliseconds()))
	if err != nil {
		return fmt.Errorf("wait for the %s service's process to exit: %w", name, err)
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("the %s service stopped but its process did not exit within %s", name, timeout)
	}
	return nil
}

func restartService(name string, stopTimeout time.Duration, terminateHung bool) error {
	if err := withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(name)
		if err != nil {
			// Not installed. Installing it is the right recovery, and mirrors the systemd
			// backend's restart-falls-back-to-load behavior.
			return errServiceNotInstalled
		}
		defer s.Close()
		return stopAndWait(s, name, stopTimeout, terminateHung)
	}); err != nil {
		if errors.Is(err, errServiceNotInstalled) {
			return startService(name)
		}
		return err
	}
	return startService(name)
}

// errServiceNotInstalled distinguishes "there is nothing to restart" from a real failure, so
// restart can fall back to starting rather than reporting an error for a recoverable state.
var errServiceNotInstalled = errors.New("service is not installed")

func queryService(name string) Status {
	status := Status{Label: name, Kind: string(KindWindowsService)}
	err := withManager(func(m *mgr.Mgr) error {
		s, err := m.OpenService(name)
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
