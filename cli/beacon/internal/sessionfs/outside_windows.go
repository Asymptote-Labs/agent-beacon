//go:build windows

package sessionfs

import (
	"io/fs"
	"os"
)

// openRegular opens a path only if it names a regular file, checked before it is opened. Windows
// confinements check no ownership, so othersCanEnter is always false: it is never consulted.
func openRegular(path string) (f *os.File, othersCanEnter bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	f, err = os.Open(path)
	return f, false, err
}
