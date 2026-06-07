package ci

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StateFileName is the sidecar session state file written by ci start and read
// by ci stop. It lives in the session base directory and never contains a
// forwarding token.
const StateFileName = "session.json"

// DefaultStatePath returns the default sidecar state file path, alongside the
// default runtime log (under $RUNNER_TEMP/beacon when available).
func DefaultStatePath() string {
	return filepath.Join(filepath.Dir(DefaultLogPath()), StateFileName)
}

// StartDetached launches the collector as a background process that outlives
// this process, waits for it to become ready, and records its PID on the
// session. Collector output is written to logWriter. Use this for the sidecar
// lifecycle (ci start); Start is the foreground variant used by ci exec.
func (s *Session) StartDetached(logWriter io.Writer) (int, error) {
	if s == nil {
		return 0, errors.New("ci session is nil")
	}
	if logWriter == nil {
		logWriter = io.Discard
	}
	cmd := collectorCommand(s.CollectorBinary, "--config", s.ConfigPath)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	s.PID = cmd.Process.Pid
	// Reap the child if it exits while this (short-lived) process is still
	// alive, so it does not linger as a zombie. In normal use ci start exits
	// right after returning and the running collector is reparented to init,
	// which reaps it after ci stop signals it by PID.
	go func() { _ = cmd.Wait() }()
	if err := waitCollectorReady(s.cfg, 10*time.Second); err != nil {
		_ = s.StopDetached(5 * time.Second)
		s.PID = 0
		return 0, err
	}
	return s.PID, nil
}

// StopDetached terminates a previously detached collector by PID, escalating to
// kill if it does not exit within the timeout. It is a no-op when no PID is set.
func (s *Session) StopDetached(timeout time.Duration) error {
	if s == nil || s.PID <= 0 {
		return nil
	}
	proc, err := os.FindProcess(s.PID)
	if err != nil {
		return nil
	}
	_ = terminateProcess(proc)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(s.PID) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return proc.Kill()
}

// WriteState persists the session as JSON so a later ci stop can find and stop
// the collector and validate the log. The state never includes a forwarding
// token.
func (s *Session) WriteState(path string) error {
	if s == nil {
		return errors.New("ci session is nil")
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadSession reads a sidecar session state file written by WriteState.
func LoadSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// processAlive reports whether a process with the given PID exists. Signal 0
// performs an existence check on Unix; on platforms where it is unsupported the
// process is treated as gone (the collector is only built for Unix targets).
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
