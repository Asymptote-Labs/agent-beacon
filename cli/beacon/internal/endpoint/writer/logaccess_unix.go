//go:build unix

package writer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

// logOwnerUID owns a restricted log; tests that cannot be root use their own uid.
var logOwnerUID = 0

// restrictLockWait bounds how long RestrictSystemLog waits for the log's lock.
var restrictLockWait = 5 * time.Second

// RestrictSystemLog puts the system log at path in the restricted access mode: the directory
// becomes root:gid 02750, the marker that tells every root writer the mode is written, and the log
// and its archives become root:gid 0640 and its lock root-only 0600. From then on only root writes
// the log and only root and the readers group read it; hooks reach it through the collector's
// ingest socket.
//
// The marker is written first, so a root writer that opens the log in between already creates or
// settles it in the restricted mode rather than widening it back. Hooks that try to append
// directly in the meantime are refused and spool their events until the collector serves the
// socket.
//
// A log that was shared is not narrowed in place. Any account could have opened it, or its lock,
// while it was 0666, and a descriptor opened then keeps working whatever the file's mode becomes:
// it could still rewrite events, read new ones, or hold the lock and stall the collector. So each
// file is copied into a new root-created file with the restricted mode and renamed over the old
// one, under the log's lock, leaving any such descriptor on an inode nothing writes or reads any
// more. A log already restricted, whose files already carry the right owner and mode, is left as it
// is, so repair and upgrades do not copy the log every time.
func RestrictSystemLog(path string, gid int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create the log directory %s: %w", dir, err)
	}
	if err := ensureRealDirectory(dir); err != nil {
		return err
	}
	if err := os.Lchown(dir, logOwnerUID, gid); err != nil {
		return fmt.Errorf("set the owner of %s: %w", dir, err)
	}
	if err := os.Chmod(dir, asymptoteobserve.RestrictedRuntimeLogDirMode); err != nil {
		return fmt.Errorf("set the mode of %s: %w", dir, err)
	}
	wasRestricted := asymptoteobserve.RuntimeLogAccessRestricted(path)
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessRestricted); err != nil {
		return fmt.Errorf("record the restricted mode: %w", err)
	}

	// Writers append under the lock, so holding it keeps a line from landing in the old log after
	// its copy was taken. The lock itself is replaced last, while still held.
	// The wait is bounded: while the log was shared any account could take that lock and keep it,
	// and the restriction must not hang on it. Past the bound it goes ahead unlocked, at the risk
	// of one line written in between going to the old file.
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer lock.Close()
		ctx, cancel := context.WithTimeout(context.Background(), restrictLockWait)
		held, err := filelock.ExclusiveContext(ctx, lock)
		cancel()
		if err == nil {
			defer held.Release()
		}
	}
	base := filepath.Base(path)
	return forLogFamily(path, func(name string) error {
		mode := asymptoteobserve.RestrictedRuntimeLogFileMode
		if name == base+".lock" {
			mode = asymptoteobserve.RestrictedRuntimeLogLockMode
		}
		return restrictOne(filepath.Join(dir, name), mode, gid, wasRestricted)
	}, base+".lock")
}

// restrictOne gives the file at path the restricted owner and mode: in place when the log was
// already restricted and the file already matches, and otherwise by replacing it with a copy.
func restrictOne(path string, mode os.FileMode, gid int, wasRestricted bool) error {
	src, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, os.ErrNotExist) {
			return nil // a link where a log file belongs is left alone
		}
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the file's owner")
	}
	if wasRestricted && int(stat.Uid) == logOwnerUID && int(stat.Gid) == gid && info.Mode().Perm() == mode && stat.Nlink == 1 {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".restrict-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chown(logOwnerUID, gid); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ShareSystemLog returns the system log at path to the shared access mode it had before the
// restricted mode existed: the marker goes, the directory is 0755, and the live log and its lock
// are writable by everyone again so hooks can append directly. Archives become 0644: no writer ever
// appends to them. Group ownership is left as it is; it grants nothing the mode does not.
//
// A no-op beyond EnsureSystemLogWritable for a log that was never restricted, whose files already
// carry those modes.
func ShareSystemLog(path string) error {
	wasRestricted := asymptoteobserve.RuntimeLogAccessRestricted(path)
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessShared); err != nil {
		return fmt.Errorf("remove the restricted mode marker: %w", err)
	}
	if err := EnsureSystemLogWritable(path); err != nil {
		return err
	}
	if !wasRestricted {
		return nil
	}
	return settleLogFamily(path, func(f *os.File) error {
		mode := os.FileMode(0o644)
		if name := filepath.Base(f.Name()); name == filepath.Base(path) || name == filepath.Base(path)+".lock" {
			mode = asymptoteobserve.SharedRuntimeLogFileMode
		}
		return f.Chmod(mode)
	})
}

func ensureRealDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// settleLogFamily applies fn to the log at path, its lock and every numbered archive beside it,
// opening each without following a link and skipping anything that is not a regular file with a
// single link -- the same refusals the root writers apply, so the directory cannot steer this into
// changing the owner or mode of some other file.
func settleLogFamily(path string, fn func(*os.File) error) error {
	dir := filepath.Dir(path)
	return forLogFamily(path, func(name string) error {
		return settleOne(filepath.Join(dir, name), fn)
	})
}

// forLogFamily calls fn with the name of the log at path, its lock and every numbered archive in
// its directory. Names listed in last are visited after the others.
func forLogFamily(path string, fn func(name string) error, last ...string) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names, deferred []string
	for _, entry := range entries {
		name := entry.Name()
		if !inLogFamily(base, name) {
			continue
		}
		if slices.Contains(last, name) {
			deferred = append(deferred, name)
			continue
		}
		names = append(names, name)
	}
	var errs []error
	for _, name := range append(names, deferred...) {
		if err := fn(name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func inLogFamily(base, name string) bool {
	if name == base || name == base+".lock" {
		return true
	}
	suffix, ok := strings.CutPrefix(name, base+".")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(suffix)
	return err == nil && n > 0
}

func settleOne(path string, fn func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil // a link where a log file belongs is left alone
		}
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		return nil
	}
	return fn(f)
}
