package service

import (
	"fmt"
	"runtime"
)

// windowsBackend manages the collector through the Windows Service Control Manager.
//
// Mapping from the two backends it joins:
//
//	RunAtLoad / WantedBy=multi-user.target -> StartType automatic, plus an explicit Start
//	KeepAlive / Restart=always             -> SCM recovery actions: restart, with a delay
//	StandardOutPath / journal              -> the collector's own file logging; the SCM
//	                                          captures no stdout, so there is nothing to redirect
//	a plist or unit file                   -> no file at all; the definition lives in the
//	                                          registry, which unitPath reports instead
//
// The collector is registered directly, with no service host in front of it. That is possible
// because the builder-generated main_windows.go calls svc.Run(otelcol.NewSvcHandler(params)) and
// falls back to running interactively when it is not launched by the SCM, so one binary serves
// both. Whether it really speaks the control protocol is not taken on trust -- the Windows sandbox
// registers a scratch service and polls for RUNNING before this backend is relied on.
//
// System mode only. Manager routes user mode to the supervised backend, because Windows has no
// per-user service manager to route it to.
type windowsBackend struct{}

func (windowsBackend) kind() Kind { return KindWindowsService }

func (windowsBackend) available() bool {
	return runtime.GOOS == "windows"
}

func (windowsBackend) unsupportedReason() string {
	if runtime.GOOS != "windows" {
		return "Windows service management is available only on Windows"
	}
	// Reaching the SCM needs administrator rights. Reported as a reason rather than surfacing
	// "Access is denied" from three call sites, since the fix is the same for all of them.
	return "the Windows Service Control Manager could not be opened; run this from an elevated shell"
}

// label is the SCM service name. Both scopes report the same name because only system mode reaches
// this backend at all -- user mode is routed to supervised before it gets here.
func (windowsBackend) label(userMode bool) string { return WindowsServiceName }

// unitPath reports the registry key that holds the service definition.
//
// There is no unit file to point at, and callers show this to a human, so naming where the
// definition actually lives beats returning an error or an empty string. The supervised backend
// makes the same trade by reporting its pidfile.
func (windowsBackend) unitPath(userMode bool) (string, error) {
	return `HKLM\SYSTEM\CurrentControlSet\Services\` + WindowsServiceName, nil
}

func (b windowsBackend) writeUnit(userMode bool, program, configPath string) (string, error) {
	if !b.available() {
		return "", fmt.Errorf("%s", b.unsupportedReason())
	}
	if err := createOrUpdateService(program, configPath); err != nil {
		return "", err
	}
	return b.unitPath(userMode)
}

func (b windowsBackend) load(userMode bool) error {
	if !b.available() {
		return fmt.Errorf("%s", b.unsupportedReason())
	}
	return startService()
}

// unload stops and deletes the service, tolerating its absence.
//
// Uninstall and repair both call this speculatively, so a missing service is success rather than
// an error -- the same contract the launchd and systemd backends keep.
func (b windowsBackend) unload(userMode bool) error {
	if !b.available() {
		return nil
	}
	return removeService()
}

func (b windowsBackend) restart(userMode bool) error {
	if !b.available() {
		return fmt.Errorf("%s", b.unsupportedReason())
	}
	return restartService()
}

func (b windowsBackend) status(userMode bool) Status {
	status := Status{Label: WindowsServiceName}
	if !b.available() {
		status.Message = b.unsupportedReason()
		return status
	}
	return queryService()
}
