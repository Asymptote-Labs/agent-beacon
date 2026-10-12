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

// elevatedOpenFlags are added when root opens a runtime log: never follow a symlink in the last
// component, and never block opening a FIFO.
func elevatedOpenFlags() int {
	if os.Geteuid() != 0 {
		return 0
	}
	return syscall.O_NOFOLLOW | syscall.O_NONBLOCK
}
