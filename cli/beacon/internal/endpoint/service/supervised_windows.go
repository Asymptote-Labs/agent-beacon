//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a process that has not exited.
//
// Spelled out because neither syscall nor x/sys/windows exports it under that name, and 259 as a
// bare literal in the comparison below would read as arbitrary. It is also why a collector must
// never exit with 259 of its own accord -- that would be indistinguishable from still running,
// which is a documented Win32 caveat rather than something this code can defend against.
const stillActive = 259

// detachAttrs starts the collector detached from this process's console.
//
// The POSIX equivalent is setsid, and the goal is the same: the collector has to outlive the CLI
// invocation that started it. On Windows a child shares its parent's console by default and
// receives that console's Ctrl+C and Ctrl+Break, so closing the shell that ran install would take
// the collector with it.
//
// Both flags are needed and they do different things. DETACHED_PROCESS withholds the parent's
// console. CREATE_NEW_PROCESS_GROUP makes the child the root of its own group, which is also what
// makes a targeted Ctrl+Break possible later -- see terminateGracefully.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// terminateGracefully asks the collector to shut down cleanly.
//
// Windows has no SIGTERM. The closest equivalent for a console program is a Ctrl+Break event sent
// to its process group, which the Go runtime in the child translates into os.Interrupt -- so the
// collector runs its normal shutdown and the exporter closes its file. That matters here rather
// than being a nicety: killing outright loses whatever the exporter had buffered, which shows up as
// missing events at the end of a log.
//
// Sent to the child's own group, which detachAttrs created. Passing 0 would signal *this* process's
// group and take the CLI down with the collector.
//
// A failure is returned rather than swallowed, but it is not fatal to the caller: unload falls back
// to Kill after a grace period, which is the correct escalation when a process will not stop
// politely -- and on Windows a process with no console at all cannot receive this event.
func terminateGracefully(proc *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(proc.Pid))
}

// pidAlive reports whether a pid is live.
//
// Signal 0 does not exist on Windows, and os.FindProcess always succeeds there, so the POSIX trick
// reports every pid as alive -- which would make status claim a dead collector is running. The
// process is opened and its exit code queried instead: STILL_ACTIVE means running.
//
// A pid that cannot be opened is reported dead. That is the right answer for the common cases (the
// process exited, or the pid was recycled and is now something this user cannot touch), and the
// alternative -- assuming alive when we cannot tell -- would leave `endpoint install` refusing to
// start a collector that is not actually there.
func pidAlive(pid int) bool {
	// PROCESS_QUERY_LIMITED_INFORMATION rather than PROCESS_QUERY_INFORMATION: it is the narrower
	// right and is granted for processes this one may not fully inspect, so a collector running as
	// another user is still visible.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// pidRunsProgram reports whether pid's image is program, so a pid Windows has handed to another
// process since (after a reboot or sign-out it often has) is never reported running, and never
// stopped. A pid whose image cannot be read is not ours to stop: a supervised collector or
// forwarder runs as this user, whose own processes can always be queried.
func pidRunsProgram(pid int, program string) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return false
	}
	return sameWindowsPath(windows.UTF16ToString(buf[:size]), program)
}

// sameWindowsPath compares two paths as Windows does: case-insensitively, after cleaning, and
// through any link in program's path (the image name is the resolved one).
func sameWindowsPath(image, program string) bool {
	if strings.EqualFold(filepath.Clean(image), filepath.Clean(program)) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(program)
	return err == nil && strings.EqualFold(filepath.Clean(image), filepath.Clean(resolved))
}
