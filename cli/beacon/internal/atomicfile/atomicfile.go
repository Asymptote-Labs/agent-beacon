// Package atomicfile replaces files so readers never observe partial content.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write writes via a temp file in the same directory and renames it over path, so a
// concurrent reader or a crash never sees a half-written file. The mode is set before
// the rename so the content is never readable with wider permissions.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	return finish(tmp, path, data)
}

// WriteSystemPrivate replaces path like Write, for a file in the machine-wide Beacon tree that only
// the system may read: config.json or the collector config holding a HEC token, a Beacon Cloud
// credential. On POSIX that is Write with 0600, since the writer is root there. On Windows the mode
// would change nothing but the read-only attribute and the file would inherit the tree's Users read
// grant, so the temp file is created with an ACL for SYSTEM and Administrators alone, and the
// rename keeps it.
func WriteSystemPrivate(path string, data []byte) error {
	tmp, err := createSystemPrivateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	return finish(tmp, path, data)
}

// finish writes data to tmp, closes it and renames it over path, removing it on any failure.
func finish(tmp *os.File, path string, data []byte) error {
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
