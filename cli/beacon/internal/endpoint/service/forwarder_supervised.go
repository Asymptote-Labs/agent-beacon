package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// A user-mode forwarder on Windows runs as a supervised Vector process, the way a user-mode
// collector does there (see Manager.backend): Windows has no per-user service manager, so the
// process is started detached and tracked by a pidfile in the endpoint's base directory. It carries
// the collector's caveats and status names them: nothing restarts it if it exits, and Windows ends
// it at sign-out. Install and repair (and `beacon endpoint asymptote reconcile`) start it again,
// as they start the collector.

// supervisedForwarderStartCheck is how long Load watches a freshly started Vector before calling it
// started: one that rejects its config or cannot open its data directory exits within it. Tests
// shorten it.
var supervisedForwarderStartCheck = 2 * time.Second

// supervisedForwarderStopTimeout bounds Unload's wait for Vector to exit before ending it.
var supervisedForwarderStopTimeout = 10 * time.Second

// windowsUserSupervised reports whether this manager runs the forwarder as a supervised process:
// a user-mode forwarder on the Windows service backend whose spec offers one.
func (m ForwarderManager) windowsUserSupervised() bool {
	return m.resolvedKind() == KindWindowsService && m.UserMode && m.spec().SupervisedName != ""
}

func (m ForwarderManager) supervisedPidPath() (string, error) {
	return filepath.Join(stateDir(true), m.spec().SupervisedName+".pid"), nil
}

func (m ForwarderManager) supervisedLogPath() string {
	return filepath.Join(stateDir(true), m.spec().SupervisedName+".out")
}

func (m ForwarderManager) readSupervised() (supervisedState, error) {
	var st supervisedState
	path, _ := m.supervisedPidPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func (m ForwarderManager) writeSupervised(st supervisedState) error {
	path, _ := m.supervisedPidPath()
	if err := ensureDir(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeSupervisedUnit records the command Load starts, keeping track of a forwarder already running.
func (m ForwarderManager) writeSupervisedUnit(vectorBin, configPath string) (string, error) {
	path, _ := m.supervisedPidPath()
	existing, _ := m.readSupervised()
	st := supervisedState{Program: vectorBin, ConfigPath: configPath}
	if existing.running() {
		st.PID, st.StartedAt, st.StartedProgram = existing.PID, existing.StartedAt, existing.StartedProgram
	}
	return path, m.writeSupervised(st)
}

func (m ForwarderManager) supervisedUnitCurrent(vectorBin, configPath string) (bool, error) {
	st, err := m.readSupervised()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(st.Program, vectorBin) && strings.EqualFold(st.ConfigPath, configPath), nil
}

// loadSupervised (re)starts Vector, so a rewritten config takes effect, and waits long enough to
// see one that exits at once.
func (m ForwarderManager) loadSupervised() error {
	if err := m.unloadSupervised(); err != nil {
		return err
	}
	st, err := m.readSupervised()
	if err != nil {
		return fmt.Errorf("no forwarder recorded; run `beacon endpoint connect`: %w", err)
	}
	if st.Program == "" {
		return errors.New("the forwarder has no recorded Vector binary; run `beacon endpoint connect`")
	}
	cmd := exec.Command(st.Program, append([]string{"--config", st.ConfigPath}, m.spec().Args...)...)
	cmd.Env = append(os.Environ(), m.spec().Env...)
	cmd.SysProcAttr = detachAttrs()
	logPath := m.supervisedLogPath()
	if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
		cmd.Stdout, cmd.Stderr = f, f
		// The child holds its own copy of the handle; this one is never written through.
		defer func() { _ = f.Close() }()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the forwarder: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	st.PID = cmd.Process.Pid
	st.StartedAt = time.Now().UTC().Format(time.RFC3339)
	st.StartedProgram = st.Program
	if err := m.writeSupervised(st); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	select {
	case err := <-exited:
		st.PID = 0
		_ = m.writeSupervised(st)
		return fmt.Errorf("the forwarder exited right after it started (%v); see %s", err, logPath)
	case <-time.After(supervisedForwarderStartCheck):
		return nil
	}
}

// unloadSupervised stops a running forwarder and keeps its record, so Load can start it again.
func (m ForwarderManager) unloadSupervised() error {
	st, err := m.readSupervised()
	if err != nil {
		return nil
	}
	if st.running() {
		if proc, err := os.FindProcess(st.PID); err == nil {
			// A detached Vector has no console, so the Ctrl+Break usually cannot reach it; then it is
			// ended at once rather than after the wait. That loses nothing the disk buffer holds, and
			// a line re-read from a checkpoint carries the event.id it was sent with.
			deadline := time.Now().Add(supervisedForwarderStopTimeout)
			if terminateGracefully(proc) != nil {
				deadline = time.Now()
			}
			for time.Now().Before(deadline) && st.running() {
				time.Sleep(supervisedStopPoll)
			}
			if st.running() {
				_ = proc.Kill()
				// Vector locks its data directory, so the next one cannot start until this has exited.
				for wait := time.Now().Add(5 * time.Second); time.Now().Before(wait) && st.running(); {
					time.Sleep(supervisedStopPoll)
				}
			}
		}
	}
	if st.PID == 0 {
		return nil
	}
	st.PID = 0
	return m.writeSupervised(st)
}

func (m ForwarderManager) removeSupervised() {
	_ = m.unloadSupervised()
	if path, err := m.supervisedPidPath(); err == nil {
		_ = os.Remove(path)
	}
}

func (m ForwarderManager) supervisedStatus() Status {
	status := Status{Label: m.Label(), Kind: string(KindSupervised)}
	if runtime.GOOS != "windows" {
		status.Message = m.UnsupportedReason()
		return status
	}
	st, err := m.readSupervised()
	if err != nil || st.Program == "" {
		status.Message = "forwarder not installed"
		return status
	}
	status.Loaded = true
	status.Running = st.running()
	if status.Running {
		status.Message = fmt.Sprintf("supervised pid %d (%s)", st.PID, supervisedCaveats(true))
	} else {
		status.Message = "recorded but not running (" + supervisedCaveats(true) + "); run `beacon endpoint repair` to start it"
	}
	return status
}
