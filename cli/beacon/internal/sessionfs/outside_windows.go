//go:build windows

package sessionfs

import (
	"io/fs"
	"os"
)

// openRegular opens a path only if it names a regular file, checked before it is opened. Windows
// confinements check no ownership or reach (their uid is negative), so reach is always allReach.
func openRegular(path string, uid int) (f *os.File, r reach, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, allReach, err
	}
	if !info.Mode().IsRegular() {
		return nil, allReach, &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	f, err = os.Open(path)
	return f, allReach, err
}
