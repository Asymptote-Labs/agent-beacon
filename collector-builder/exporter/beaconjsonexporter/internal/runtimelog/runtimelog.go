// Package runtimelog appends lines to the endpoint runtime log for the collector's components: the
// beaconjson exporter and the beacon_ingest extension, which must take the same lock, rotate the
// same way and settle the same file modes.
package runtimelog

import (
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// DefaultRotateArchives is how many archives a log keeps when the caller passes none.
const DefaultRotateArchives = asymptoteobserve.DefaultRuntimeLogRotateArchives

// Append writes one line to the runtime log at path under its lock, rotating first when the line
// would take the log past rotateBytes and dropping a line that duplicates a recent one.
//
// Keep this file-mode contract mirrored with the endpoint CLI and hook writer; the rotation itself
// is asymptoteobserve.RotateRuntimeLog for all three.
func Append(path string, line []byte, rotateBytes int64, rotateArchives int) error {
	_, dirMode, _ := asymptoteobserve.RuntimeLogModes(path)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	lock, err := openRuntimeFileAs(path+".lock", path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	held, err := filelock.Exclusive(lock)
	if err != nil {
		_ = lock.Close()
		return err
	}
	// Unlock before closing: LIFO defer ordering means the close listed first runs last.
	defer lock.Close()
	defer held.Release()
	if asymptoteobserve.IsDuplicateEndpointEvent(path, line, asymptoteobserve.EndpointDuplicateWindow) {
		return nil
	}
	// A rotation that cannot happen now (another process holds the log open without delete sharing,
	// say) is deferred, not fatal: the line goes into the live file past the size limit and a later
	// write rotates. Returning the error here used to drop the line.
	_ = asymptoteobserve.RotateRuntimeLog(path, rotateBytes, rotateArchives, int64(len(line)))
	f, err := openRuntimeFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func openRuntimeFile(path string, flag int) (*os.File, error) {
	return openRuntimeFileAs(path, path, flag)
}

// openRuntimeFileAs opens path (the log or its lock) with the mode logPath calls for, and settles
// its mode through asymptoteobserve.OpenRuntimeLogFile: a new file gets that mode whatever the
// umask, an existing one is only ever narrowed toward it, by its owner, and root never follows a
// symlink or opens anything but a regular file there.
func openRuntimeFileAs(path, logPath string, flag int) (*os.File, error) {
	return asymptoteobserve.OpenRuntimeLogFile(path, logPath, flag)
}
