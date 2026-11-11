//go:build !windows

package sessionfs

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// openRegular opens a resolved absolute path as a regular file without following a symlink and
// without opening anything else. Opening a device can act on it (opening /dev/watchdog arms the
// timer), so the last entry is checked before it is opened: each directory is opened relative to
// its parent with O_NOFOLLOW, which also stops a directory swapped for a symlink after the path
// was resolved, then the entry is lstat'ed in that directory and opened only if it is a regular
// file, non-blocking. The opened file must be the one checked.
func openRegular(path string) (*os.File, error) {
	fail := func(err error) error { return &fs.PathError{Op: "open", Path: path, Err: err} }
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if !strings.HasPrefix(path, "/") || len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, fail(fmt.Errorf("not an absolute file path"))
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fail(err)
	}
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(dir)
		if err != nil {
			return nil, fail(err)
		}
		dir = next
	}
	defer unix.Close(dir)
	name := parts[len(parts)-1]
	var checked unix.Stat_t
	if err := unix.Fstatat(dir, name, &checked, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fail(err)
	}
	if checked.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fail(errNotRegular)
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fail(err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fail(err)
	}
	opened, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(opened.Dev) != uint64(checked.Dev) || uint64(opened.Ino) != uint64(checked.Ino) {
		_ = f.Close()
		return nil, fail(fmt.Errorf("replaced while it was opened"))
	}
	return f, nil
}
