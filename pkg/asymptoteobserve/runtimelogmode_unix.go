//go:build unix

package asymptoteobserve

import (
	"os"
	"syscall"
)

// processOwnsHome reports whether a private log may be created in home by this process. Only root
// needs the check: root creating a 0600 file in a home it does not own (sudo preserving HOME) would
// leave the owner's hooks unable to append, so that case keeps the shared mode.
func processOwnsHome(home string) bool {
	if os.Geteuid() != 0 {
		return true
	}
	info, err := os.Stat(home)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0
}

// ownedByProcess reports whether this process owns the file, the only case in which a writer
// changes an existing log's mode.
func ownedByProcess(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Geteuid()
}

// runningAsRoot reports whether this process writes the log as root, the only case in which a link
// or FIFO someone planted where it appends could escalate. A variable so the refusals can be tested
// without root.
var runningAsRoot = func() bool { return os.Geteuid() == 0 }

// elevatedOpenFlags are added when root opens a runtime log: never follow a symlink in the last
// component, and never block opening a FIFO.
func elevatedOpenFlags() int {
	if !runningAsRoot() {
		return 0
	}
	return syscall.O_NOFOLLOW | syscall.O_NONBLOCK
}

// linkedElsewhere reports whether root opened a log that has another hard link. A runtime log has
// exactly one, rotation included (it renames), so a second is one someone planted: a hard link to
// a root-owned file passes O_NOFOLLOW and the regular-file check, and root would append to it.
func linkedElsewhere(info os.FileInfo) bool {
	if !runningAsRoot() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
