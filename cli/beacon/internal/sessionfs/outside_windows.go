//go:build windows

package sessionfs

import (
	"io/fs"
	"os"
)

// openRegular opens a path only if it names a regular file, checked before it is opened.
func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	return os.Open(path)
}
