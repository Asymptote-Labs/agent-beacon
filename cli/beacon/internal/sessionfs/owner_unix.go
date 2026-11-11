//go:build !windows

package sessionfs

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

const openNonblock = syscall.O_NONBLOCK

func ownerUID(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// OwnerUID reports the uid that owns path, following symlinks. The system backfill confines reads
// to files owned by whoever owns the console user's home.
func OwnerUID(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	uid, ok := ownerUID(info)
	if !ok {
		return 0, fmt.Errorf("no owner for %s", path)
	}
	return uid, nil
}
