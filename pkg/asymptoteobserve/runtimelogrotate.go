package asymptoteobserve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultRuntimeLogRotateArchives is how many archives a runtime log keeps when a writer passes
// none: path.1 through path.5 beside the live file.
const DefaultRuntimeLogRotateArchives = 5

// ErrRuntimeLogRotationDeferred reports a rotation that was not started, because a file it would
// rename or remove cannot be renamed or removed right now. Nothing moved, and the next write tries
// again.
var ErrRuntimeLogRotationDeferred = errors.New("runtime log rotation deferred")

// runtimeLogRotationProbe and runtimeLogRotationRename are the two operations a rotation depends
// on, replaceable so tests can refuse either at a chosen file.
var (
	runtimeLogRotationProbe  = runtimeLogRenamable
	runtimeLogRotationRename = os.Rename
)

// RuntimeLogNeedsRotation reports whether appending nextWriteBytes to path would take it past
// maxSize, which is when RotateRuntimeLog rotates. An empty or missing file never rotates, so one
// oversized line still lands somewhere.
func RuntimeLogNeedsRotation(path string, maxSize, nextWriteBytes int64) (bool, error) {
	if maxSize <= 0 {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Size() > 0 && info.Size()+nextWriteBytes > maxSize, nil
}

// RotateRuntimeLog rotates the runtime log at path when appending nextWriteBytes would take it past
// maxSize: the live file becomes path.1, each archive moves up one, and the archive at path.archives
// (and anything numbered past it, which no reader looks at) is removed. The caller holds the log's
// lock.
//
// Every writer of the log -- the endpoint CLI, the hook adapter and the collector -- rotates through
// this one function and appends its line whatever it returns. A rotation that cannot happen now is
// deferred rather than forced: the log grows past maxSize until a later write can rotate it. The
// writers used to return the error instead, so a rotation that failed dropped the event it was
// making room for.
//
// Nothing moves unless the whole rotation can complete. Windows refuses to rename or delete a file
// another process holds open without delete sharing, which is how PowerShell's Get-Content and
// [IO.File]::OpenRead open it, so every file the rotation would rename or remove is checked first
// and the rotation is skipped when one is held. Before that check existed, the archives had already
// moved up when the rename of the live file was refused, so each further attempt deleted one more
// archive. Elsewhere a rename ignores open handles and the check passes.
//
// No archive is renamed over another. The rotation moves archives up only as far as the lowest free
// slot, so a gap left by a rotation that failed partway (a reader opened a file between the check
// and its rename) is closed by the next rotation instead of costing an archive.
func RotateRuntimeLog(path string, maxSize int64, archives int, nextWriteBytes int64) error {
	rotate, err := RuntimeLogNeedsRotation(path, maxSize, nextWriteBytes)
	if err != nil || !rotate {
		return err
	}
	if archives < 1 {
		archives = DefaultRuntimeLogRotateArchives
	}
	archive := func(i int) string { return fmt.Sprintf("%s.%d", path, i) }

	remove, err := runtimeLogArchivesPast(path, archives)
	if err != nil {
		return err
	}
	free := 0
	for i := 1; i <= archives; i++ {
		_, err := os.Lstat(archive(i))
		if os.IsNotExist(err) {
			free = i
			break
		}
		if err != nil {
			return err
		}
	}
	if free == 0 {
		remove = append(remove, archive(archives))
		free = archives
	}

	touched := []string{path}
	for i := 1; i < free; i++ {
		touched = append(touched, archive(i))
	}
	for _, p := range append(touched, remove...) {
		if err := runtimeLogRotationProbe(p); err != nil {
			return fmt.Errorf("%w: %v", ErrRuntimeLogRotationDeferred, err)
		}
	}

	for _, p := range remove {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for i := free - 1; i >= 1; i-- {
		if err := renameRuntimeLogFile(archive(i), archive(i+1)); err != nil {
			return err
		}
	}
	if err := renameRuntimeLogFile(path, archive(1)); err != nil {
		return err
	}
	SettleRuntimeLogArchives(path, archives)
	return nil
}

// renameRuntimeLogFile renames from to to, refusing when to exists. os.Rename replaces an existing
// file on every platform, and an archive that is replaced is lost.
func renameRuntimeLogFile(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrRuntimeLogRotationDeferred, to)
	} else if !os.IsNotExist(err) {
		return err
	}
	return runtimeLogRotationRename(from, to)
}

// runtimeLogArchivesPast lists path's archives numbered past archives, which a rotation removes.
func runtimeLogArchivesPast(path string, archives int) ([]string, error) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(path) + "."
	var past []string
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index <= archives {
			continue
		}
		past = append(past, filepath.Join(dir, entry.Name()))
	}
	return past, nil
}
