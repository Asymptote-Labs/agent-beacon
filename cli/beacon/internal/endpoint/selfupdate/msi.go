package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
)

// The Windows install path. A package is installed with msiexec, which runs the MSI's own custom
// actions: the uninstall action of the product it replaces stops and removes the collector service,
// and its install action (install-endpoint.ps1) sets the endpoint up again and starts it. So there
// is no collector restart here, and no snapshot of the install tree to copy back: the files belong
// to Windows Installer, and a copy put back behind its back would leave its database naming a
// product that is not on disk. A failed install is rolled back by reinstalling the previous MSI
// (Applier.RollbackPackage) instead.

// msiexec exit codes that are not plain failures.
const (
	msiSuccessRebootRequired  = 3010 // ERROR_SUCCESS_REBOOT_REQUIRED
	msiSuccessRebootInitiated = 1641 // ERROR_SUCCESS_REBOOT_INITIATED (not with /norestart)
	msiUnknownProduct         = 1605 // ERROR_UNKNOWN_PRODUCT: /x of a product that is not installed
)

// replacedBeaconPrefix names a beacon.exe moved out of the install while it ran.
const replacedBeaconPrefix = "replaced-beacon-"

func isMSI(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".msi") }

// msiexecPath is Windows Installer's own msiexec.exe, named by path so nothing earlier on PATH
// stands in for it. Elsewhere (tests) it is the bare name.
func msiexecPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "msiexec.exe")
	}
	return "msiexec"
}

// msiError is an msiexec run that failed, with its exit code and the verbose log that says why.
type msiError struct {
	op   string
	code int
	log  string
	out  string
	err  error
}

func (e *msiError) Error() string {
	msg := fmt.Sprintf("msiexec %s failed", e.op)
	if e.code > 0 {
		msg = fmt.Sprintf("msiexec %s exited with %d", e.op, e.code)
	} else if e.err != nil {
		msg += ": " + e.err.Error()
	}
	if out := strings.TrimSpace(e.out); out != "" {
		msg += ": " + out
	}
	return msg + " (log: " + e.log + ")"
}

// exitCode is the exit code an error from the runner carries, or -1.
func exitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

// msiexec runs msiexec op (/i or /x) on pkg quietly, without letting it restart the machine, and
// with a verbose log in the staging directory named for step. A request for a restart is success:
// a file in use (a hook running beacon-hooks.exe) is replaced at the next restart, and is noted.
func (a *Applier) msiexec(ctx context.Context, step, op, pkg string) error {
	log := filepath.Join(a.stageDir(), "msiexec-"+step+".log")
	out, err := a.runner()(ctx, msiexecPath(), op, pkg, "/qn", "/norestart", "/l*v", log)
	if err == nil {
		return nil
	}
	code := exitCode(err)
	if code == msiSuccessRebootRequired || code == msiSuccessRebootInitiated {
		a.rebootPending = true
		return nil
	}
	return &msiError{op: op, code: code, log: log, out: out, err: err}
}

func (a *Applier) windowsInstallDir() string {
	if a.windowsDir != "" {
		return a.windowsDir
	}
	return WindowsInstallDir()
}

// installedWindowsBeacon is the beacon.exe the MSI installs.
func (a *Applier) installedWindowsBeacon() string {
	return filepath.Join(a.windowsInstallDir(), "bin", "beacon.exe")
}

// installMSI is installPackage's install, check and rollback for a Windows MSI.
func (a *Applier) installMSI(ctx context.Context, pkg Package, pkgPath, reinstall string, result ApplyResult, success string) (ApplyResult, error) {
	a.rebootPending = false
	aside, err := a.moveRunningBeaconAside()
	if err != nil {
		message := "move the running beacon.exe out of the way of the install: " + err.Error()
		a.emit(false, result, message)
		return result, errors.New(message)
	}

	fail := func(message string, cause error) (ApplyResult, error) {
		rollbackErr := a.rollbackMSI(ctx, pkgPath, reinstall, aside, &result)
		if rollbackErr != nil {
			message += "; rollback failed: " + rollbackErr.Error()
		}
		a.emit(false, result, message)
		a.cleanupFailedUpdate(result, message, rollbackErr)
		return result, cause
	}
	if err := a.msiexec(ctx, "install", "/i", pkgPath); err != nil {
		return fail(err.Error(), fmt.Errorf("install update: %w", err))
	}
	if err := a.healthCheckAt(ctx, a.installedWindowsBeacon(), pkg.Version, pkg.Edition); err != nil {
		return fail("post-install health check failed: "+err.Error(), fmt.Errorf("post-install health check failed: %w", err))
	}
	result.Applied = true
	result.Message = success
	if a.rebootPending {
		result.Message += "; Windows finishes replacing files that were in use at the next restart"
	}
	a.cleanupSuccessfulUpdate()
	a.emit(true, result, result.Message)
	return result, nil
}

// moveRunningBeaconAside moves the installed beacon.exe into the staging directory when it is the
// executable running this install, puts an unlocked copy of it back in its place, and returns where
// the running one went ("" when it is not). Windows does not let a running executable be replaced,
// so Windows Installer would otherwise leave the old one in place until the next restart, and the
// health check would find the version that was there before. It does let one be renamed.
//
// The copy matters as much as the rename: the upgrade removes the old product first, through its
// uninstall-endpoint.ps1, which runs the installed beacon.exe to stop the collector and forwarders
// and exits early, stopping nothing, when there is none. With the copy in place that removal runs as
// it would without this install, and Windows Installer replaces the copy, which nothing runs.
//
// The moved file is removed by the next install that finds it no longer running, and put back by a
// rollback that leaves no beacon.exe.
func (a *Applier) moveRunningBeaconAside() (string, error) {
	executable := a.executable
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err != nil {
		return "", nil
	}
	installed := a.installedWindowsBeacon()
	running, err := os.Stat(exe)
	if err != nil {
		return "", nil
	}
	target, err := os.Stat(installed)
	if err != nil || !os.SameFile(running, target) {
		return "", nil
	}
	aside := filepath.Join(a.stageDir(), fmt.Sprintf("%s%d.exe", replacedBeaconPrefix, a.clock()().UnixNano()))
	if err := os.Rename(installed, aside); err != nil {
		return "", err
	}
	if err := copyExecutable(aside, installed); err != nil {
		_ = os.Remove(installed)
		if restoreErr := os.Rename(aside, installed); restoreErr != nil {
			return "", fmt.Errorf("copy %s back: %v; and could not put it back: %w", installed, err, restoreErr)
		}
		return "", fmt.Errorf("copy %s back: %w", installed, err)
	}
	return aside, nil
}

// copyExecutable copies src to a new file at dst, never a link to it: a link would share the
// running image's lock.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

// restoreAside puts a beacon.exe moved aside back when the install directory has none.
func (a *Applier) restoreAside(aside string) {
	if aside == "" {
		return
	}
	if _, err := os.Stat(a.installedWindowsBeacon()); os.IsNotExist(err) {
		_ = os.Rename(aside, a.installedWindowsBeacon())
	}
}

// rollbackMSI undoes a failed MSI install by reinstalling the previous package (reinstall): it
// removes what the failed package left (nothing, when msiexec itself failed and Windows Installer
// rolled its own transaction back), then installs the previous one. Removing first is what makes
// the reinstall possible when the previous package is an older version, which the MSI's downgrade
// rule would otherwise refuse, and the MSI keeps the endpoint's configuration across a removal.
// It also covers a failed upgrade that had already removed the previous product, which Windows
// Installer does not bring back.
//
// Without a previous package there is nothing to reinstall, and that is reported as a failed
// rollback rather than claimed as one.
func (a *Applier) rollbackMSI(ctx context.Context, failed, reinstall, aside string, result *ApplyResult) error {
	defer a.restoreAside(aside)
	if reinstall == "" {
		return errors.New("there is no previous package to reinstall, so the machine is left as msiexec left it")
	}
	removeErr := a.msiexec(ctx, "remove-failed", "/x", failed)
	var me *msiError
	if errors.As(removeErr, &me) && me.code == msiUnknownProduct {
		removeErr = nil
	}
	if err := a.msiexec(ctx, "rollback", "/i", reinstall); err != nil {
		if removeErr != nil {
			return fmt.Errorf("removing the failed package: %v; reinstalling the previous one: %w", removeErr, err)
		}
		return fmt.Errorf("reinstalling the previous package: %w", err)
	}
	result.RolledBack = true
	return nil
}

// healthCheckAt confirms the beacon at bin reports the expected version (and edition) and, in
// production, that the collector service is running.
func (a *Applier) healthCheckAt(ctx context.Context, bin, wantVersion, wantEdition string) error {
	out, err := a.runner()(ctx, bin, "version")
	if err != nil {
		return fmt.Errorf("run %s version: %s: %w", bin, strings.TrimSpace(out), err)
	}
	if !VersionLineMatches(out, wantVersion) {
		return fmt.Errorf("installed binary reports %q, expected version %s", strings.TrimSpace(out), wantVersion)
	}
	if wantEdition != "" && VersionLineEdition(out) != wantEdition {
		return fmt.Errorf("installed binary reports %q, expected the %s edition", strings.TrimSpace(out), wantEdition)
	}
	if a.AllowInsecureTest {
		return nil
	}
	running := a.collectorRunning
	if running == nil {
		running = func() bool { return service.Manager{UserMode: false}.Status().Running }
	}
	deadline := a.clock()().Add(collectorHealthTimeout)
	for {
		if running() {
			return nil
		}
		if !a.clock()().Before(deadline) {
			return fmt.Errorf("collector service is not running after update")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
