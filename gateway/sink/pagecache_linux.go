//go:build linux

package sink

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// releasePages tells the kernel it may drop f's first dropLen bytes from the page cache, all of it
// handed to the disk by earlier calls (pages still being written are left alone, and caught by a
// later call), and starts writing [writeOff, writeOff+writeLen) to disk without waiting for it.
// Both are hints: errors are ignored, and a file without a descriptor is left to the kernel.
func releasePages(f any, dropLen, writeOff, writeLen int64) {
	sc, ok := f.(syscall.Conn)
	if !ok {
		return
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		if dropLen > 0 {
			_ = unix.Fadvise(int(fd), 0, dropLen, unix.FADV_DONTNEED)
		}
		if writeLen > 0 {
			_ = unix.SyncFileRange(int(fd), writeOff, writeLen, unix.SYNC_FILE_RANGE_WRITE)
		}
	})
}
