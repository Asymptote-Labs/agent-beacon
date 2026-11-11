//go:build windows

package sessionfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

// openPublic opens a path only if it names a regular file, checked before it is opened. Windows
// confinements check no ownership (their uid is negative), so nothing more is asked of it.
func openPublic(path string) (*os.File, error) {
	if _, err := resolvePublic(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

// resolvePublic is path with symlinks resolved, when it names a regular file.
func resolvePublic(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	return filepath.EvalSymlinks(path)
}
