//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// procRoot is where procfs is mounted. A variable so tests can point it at a fixture.
var procRoot = "/proc"

// pidRunsProgram reports whether pid's command line contains program.
//
// Every argument is compared, not only argv[0]: a collector started through an interpreter (a
// shebang script, as the sandbox's stand-in is) has the interpreter in argv[0] and the program in
// argv[1]. The loader starts the program by the exact path it recorded, so an exact match is the
// right test.
//
// Without procfs (macOS) there is nothing to ask, and this reports true so the signal-0 check stays
// the only test, as it was before. With procfs, a pid it will not show us is not our collector:
// supervised mode runs the collector as the user who installed it, and that user can read its own
// processes' command lines.
func pidRunsProgram(pid int, program string) bool {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "cmdline")
	for attempt := 0; ; attempt++ {
		data, err := os.ReadFile(path)
		if err != nil {
			if _, statErr := os.Stat(filepath.Join(procRoot, "self")); statErr != nil {
				return true
			}
			return false
		}
		// An empty cmdline is what a process shows between exec's point of no return and the
		// kernel publishing its new argv. cmd.Start returns inside that window -- it waits only
		// for the close-on-exec pipe, which closes before the arguments are set up -- so reading
		// straight after starting the collector sees nothing and would call it someone else's
		// process. Give the exec a moment to finish before deciding.
		if len(data) == 0 && attempt < cmdlineRetries {
			time.Sleep(cmdlineRetryDelay)
			continue
		}
		for _, arg := range strings.Split(string(data), "\x00") {
			if arg == program {
				return true
			}
		}
		return false
	}
}

// cmdlineRetries and cmdlineRetryDelay bound the wait for an exec in progress, about half a
// second in all. Variables so tests can shorten them.
var (
	cmdlineRetries    = 50
	cmdlineRetryDelay = 10 * time.Millisecond
)
