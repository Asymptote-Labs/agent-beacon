//go:build !windows

package dashboard

import (
	"fmt"
	"os"
	"syscall"
)

// openLogFile opens a runtime log file for reading.
func openLogFile(path string) (*os.File, error) {
	return os.Open(path)
}

// logFileIdentity is the device and inode, which survive the rename rotation does.
func logFileIdentity(f *os.File) (uint64, uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("no file identity for %s", f.Name())
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
