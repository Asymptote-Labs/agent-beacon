//go:build !windows

package service

import (
	"fmt"
	"time"
)

// serviceStopTimeout matches the Windows value so callers compile everywhere; nothing waits on it here.
const serviceStopTimeout = 20 * time.Second

// The Service Control Manager exists only on Windows, so these report rather than pretend.
//
// windowsBackend.available() already gates every one of them, and Manager checks available()
// before dispatching -- so reaching one of these means a caller constructed the backend directly
// with an explicit --service=windows-service on the wrong platform. That deserves the same
// actionable error the launchd backend gives off macOS, not a silent no-op: an install that
// reports success while registering nothing is the failure mode this whole package is shaped
// around avoiding.

func errNotWindows(action string) error {
	return fmt.Errorf("cannot %s a Windows service on this platform; "+
		"use --service=auto, or systemd/launchd/none as appropriate", action)
}

func createOrUpdateService(scmService) error { return errNotWindows("register") }

func startService(string) error { return errNotWindows("start") }

func waitForServiceRunning(string, time.Duration) error { return errNotWindows("start") }

func serviceCurrent(scmService) (bool, error) { return false, errNotWindows("inspect") }

// removeService is the one exception, and deliberately so: uninstall and repair call unload
// speculatively on every backend, and failing there would block removing an endpoint that was
// never a Windows service to begin with.
func removeService(string, time.Duration, bool) error { return nil }

func restartService(string, time.Duration, bool) error { return errNotWindows("restart") }

func queryService(name string) Status {
	return Status{
		Label:   name,
		Message: "Windows service management is available only on Windows",
	}
}
