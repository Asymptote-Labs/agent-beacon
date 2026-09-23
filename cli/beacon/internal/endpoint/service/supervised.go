package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// supervisedBackend runs the collector as a detached child process tracked by a pidfile.
//
// There is no OS service manager involved, which is exactly right for containers, CI runners,
// and cloud agent sandboxes -- environments where an init system either does not exist or is
// not ours to configure. `beacon ci` has always worked this way; this makes the same model
// reachable from `beacon endpoint install`.
//
// Honest limitation: nothing restarts the collector if it dies, because there is no
// supervisor. That is a real difference from launchd's KeepAlive and systemd's
// Restart=always, and it is why this is the fallback rather than the default. Status reports
// it so nobody is misled.
type supervisedBackend struct{}

// supervisedState is what the pidfile holds. The config path is recorded so Restart can
// relaunch with the same arguments without the caller having to supply them again.
type supervisedState struct {
	PID        int    `json:"pid"`
	Program    string `json:"program"`
	ConfigPath string `json:"config_path"`
	StartedAt  string `json:"started_at"`
	// StartedProgram is the program PID was started as. It differs from Program after writeUnit
	// records a new program while the old collector is still running, and running() has to check
	// the pid against what it was started as or unload would leave the old collector alive.
	StartedProgram string `json:"started_program,omitempty"`
}

func (supervisedBackend) kind() Kind { return KindSupervised }

// available is unconditionally true: starting a child process needs no platform support.
func (supervisedBackend) available() bool { return true }

func (supervisedBackend) unsupportedReason() string { return "" }

func (supervisedBackend) label(userMode bool) string {
	if userMode {
		return "beacon-collector-user"
	}
	return "beacon-collector"
}

// unitPath reports the pidfile, since supervised mode has no unit file. Callers show this to
// users, so returning something real beats returning an error.
func (b supervisedBackend) unitPath(userMode bool) (string, error) {
	return filepath.Join(stateDir(userMode), "collector.pid"), nil
}

// writeUnit records the intended command. Nothing starts here: load does that, matching the
// other backends where writeUnit installs a definition and load activates it.
func (b supervisedBackend) writeUnit(userMode bool, program, configPath string) (string, error) {
	path, err := b.unitPath(userMode)
	if err != nil {
		return "", err
	}
	if err := ensureDir(path); err != nil {
		return "", err
	}
	existing, _ := b.read(userMode)
	state := supervisedState{Program: program, ConfigPath: configPath}
	// Preserve a live pid so writeUnit followed by status does not lose track of a collector
	// that is already running.
	if existing.running() {
		state.PID = existing.PID
		state.StartedAt = existing.StartedAt
		state.StartedProgram = existing.StartedProgram
	}
	return path, b.write(userMode, state)
}

func (b supervisedBackend) read(userMode bool) (supervisedState, error) {
	var st supervisedState
	path, err := b.unitPath(userMode)
	if err != nil {
		return st, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	// Tolerate a bare pid, which is what a hand-written or legacy pidfile looks like.
	trimmed := strings.TrimSpace(string(data))
	if pid, convErr := strconv.Atoi(trimmed); convErr == nil {
		st.PID = pid
		return st, nil
	}
	return st, json.Unmarshal(data, &st)
}

func (b supervisedBackend) write(userMode bool, st supervisedState) error {
	path, err := b.unitPath(userMode)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Written to a temporary file and renamed into place. os.WriteFile truncates and then writes,
	// so a crash in between left an empty pidfile and lost track of a running collector.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".collector.pid.*")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (b supervisedBackend) load(userMode bool) error {
	st, err := b.read(userMode)
	if err != nil {
		return fmt.Errorf("no supervised collector recorded; run endpoint install first: %w", err)
	}
	if st.Program == "" {
		return errors.New("supervised collector has no recorded program path")
	}
	if st.running() {
		return nil // already running
	}

	cmd := exec.Command(st.Program, "--config", st.ConfigPath)
	// Detach from this process group so the collector outlives the CLI invocation that
	// started it. Without this, the collector would die with the shell that ran install.
	cmd.SysProcAttr = detachAttrs()
	// Logs go to a file next to the runtime log, since there is no journal here.
	logPath := supervisedLogPath(userMode)
	if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
		cmd.Stdout = f
		cmd.Stderr = f
		// Discarding this Close error is correct rather than sloppy, which is worth saying because
		// a write handle closed without checking normally is a data-loss bug. cmd.Start duplicates
		// the descriptor into the child, so this handle is the parent's copy and the parent never
		// writes a byte through it -- there is nothing buffered for a close to fail to flush. The
		// child holds the descriptor that matters and keeps it for its lifetime. Closing here is
		// what stops the parent leaking a descriptor.
		defer func() { _ = f.Close() }()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start supervised collector: %w", err)
	}
	// Release the child so it is not left as a zombie when this process exits.
	go func() { _ = cmd.Wait() }()

	st.PID = cmd.Process.Pid
	st.StartedAt = time.Now().UTC().Format(time.RFC3339)
	st.StartedProgram = st.Program
	return b.write(userMode, st)
}

func (b supervisedBackend) unload(userMode bool) error {
	st, err := b.read(userMode)
	if err != nil {
		return nil // nothing recorded, nothing to stop
	}
	if !st.running() {
		st.PID = 0
		_ = b.write(userMode, st)
		return nil
	}
	proc, err := os.FindProcess(st.PID)
	if err != nil {
		return nil
	}
	// Ask the collector to flush and shut down cleanly; the exporter closes its file on
	// shutdown, so killing outright risks losing buffered events. What "ask" means is
	// platform-specific -- see terminateGracefully.
	_ = terminateGracefully(proc)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !st.running() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.running() {
		_ = proc.Kill()
	}
	st.PID = 0
	return b.write(userMode, st)
}

func (b supervisedBackend) restart(userMode bool) error {
	if err := b.unload(userMode); err != nil {
		return err
	}
	return b.load(userMode)
}

func (b supervisedBackend) status(userMode bool) Status {
	status := Status{Label: b.label(userMode)}
	st, err := b.read(userMode)
	if err != nil {
		status.Message = "no supervised collector recorded"
		return status
	}
	// "Loaded" means installed and intended to run, matching the other backends.
	status.Loaded = st.Program != ""
	status.Running = st.running()
	switch {
	case status.Running:
		status.Message = fmt.Sprintf("supervised pid %d (%s)", st.PID, supervisedCaveats(userMode))
	case status.Loaded:
		status.Message = "recorded but not running (" + supervisedCaveats(userMode) + ")"
	default:
		status.Message = "no supervised collector recorded"
	}
	return status
}

// supervisedCaveats names what this backend cannot promise, for status to report in-band.
//
// Stated wherever an installed collector is described, not only while one happens to be running:
// an operator reading `endpoint status` on a configured-but-stopped endpoint is exactly the person
// who needs to know nothing will start it again.
//
// Logout is a Windows-only caveat and is named only there, because it is only true there. A
// detached process on POSIX has its own session -- that is what setsid buys -- and outlives the
// login that started it. Windows terminates the session's processes at logoff, so a user-mode
// collector there stops when the person walks away, which is a materially worse guarantee and one
// they should not have to discover from an empty log.
func supervisedCaveats(userMode bool) string {
	const noRestart = "no automatic restart: no service manager"
	if userMode && runtime.GOOS == "windows" {
		return noRestart + "; does not survive logout"
	}
	return noRestart
}

// running reports whether the recorded pid is live and is still the collector this state recorded.
//
// A pid is only a number, and the kernel hands it out again once the process holding it exits. The
// pidfile outlives the collector -- an OOM kill, a container restart, a reboot of a host with no
// service manager -- so the number can name an unrelated process by the time anything reads it.
// Liveness alone then made status report a dead collector as running, load skip starting one, and
// unload send that stranger SIGTERM, then SIGKILL if it had not exited ten seconds later.
//
// A pidfile written before StartedProgram existed cannot be checked, and is trusted on liveness
// alone as before. Program is not a substitute: writeUnit may have changed it since the pid started.
func (st supervisedState) running() bool {
	if st.PID <= 0 || !pidAlive(st.PID) {
		return false
	}
	return st.StartedProgram == "" || pidRunsProgram(st.PID, st.StartedProgram)
}

// supervisedLogPath is the file a supervised collector's stdout and stderr are appended to.
func supervisedLogPath(userMode bool) string {
	return filepath.Join(stateDir(userMode), "collector.out")
}
