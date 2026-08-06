//go:build !windows

package service

import (
	"os"
	"syscall"
)

// detachAttrs puts the collector in its own session so it outlives the CLI invocation that
// started it. Without it the collector dies with the shell that ran install.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminateGracefully asks the collector to shut down cleanly.
func terminateGracefully(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

// pidAlive reports whether a pid is live. Signal 0 performs the permission and existence checks
// without delivering anything.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
