//go:build !windows

package service

import "fmt"

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

func createOrUpdateService(program, configPath string) error { return errNotWindows("register") }

func startService() error { return errNotWindows("start") }

// removeService is the one exception, and deliberately so: uninstall and repair call unload
// speculatively on every backend, and failing there would block removing an endpoint that was
// never a Windows service to begin with.
func removeService() error { return nil }

func restartService() error { return errNotWindows("restart") }

func queryService() Status {
	return Status{
		Label:   WindowsServiceName,
		Message: "Windows service management is available only on Windows",
	}
}
