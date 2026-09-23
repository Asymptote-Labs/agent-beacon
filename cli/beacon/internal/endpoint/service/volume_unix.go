//go:build !windows

package service

import (
	"os"
	"syscall"
)

// volumeID returns the device number of the filesystem holding path, following symlinks.
func volumeID(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	// Dev is int32 on darwin and uint64 on linux; the conversion is a no-op on the latter.
	return uint64(st.Dev), nil
}

// fileOwner returns the uid that owns info's file.
func fileOwner(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
