// Package runtimelog appends lines to the endpoint runtime log for the collector's components: the
// beaconjson exporter and the beacon_ingest extension, which must take the same lock, rotate the
// same way and settle the same file modes.
package runtimelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// DefaultRotateArchives is how many archives a log keeps when the caller passes none.
const DefaultRotateArchives = 5

// Append writes one line to the runtime log at path under its lock, rotating first when the line
// would take the log past rotateBytes and dropping a line that duplicates a recent one.
//
// Keep this rotation and file-mode contract mirrored with the endpoint CLI and hook writer.
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
	if err := rotateIfNeeded(path, rotateBytes, rotateArchives, int64(len(line))); err != nil {
		return err
	}
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

func rotateIfNeeded(path string, maxSize int64, archives int, nextWriteBytes int64) error {
	if maxSize <= 0 {
		return nil
	}
	if archives < 1 {
		archives = DefaultRotateArchives
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() == 0 || info.Size()+nextWriteBytes <= maxSize {
		return nil
	}
	if err := removeOverflowArchives(path, archives); err != nil {
		return err
	}
	for i := archives - 1; i >= 1; i-- {
		from := path + fmt.Sprintf(".%d", i)
		to := path + fmt.Sprintf(".%d", i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return err
	}
	asymptoteobserve.SettleRuntimeLogArchives(path, archives)
	return nil
}

func removeOverflowArchives(path string, archives int) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := base + "."
	for _, entry := range entries {
		name := entry.Name()
		suffix, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil || index < archives {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
