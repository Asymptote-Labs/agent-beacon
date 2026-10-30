//go:build windows

package writer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/loggroup"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

var (
	// logReadersSID is the SID of the local group that may read a restricted log. A variable so
	// tests can name a group without creating one.
	logReadersSID = func() (string, error) {
		sid, found, err := loggroup.SID(loggroup.Name)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("the %s group does not exist", loggroup.Name)
		}
		return sid, nil
	}
	// restrictLockWait bounds how long RestrictSystemLog waits for the log's lock, and replaceWait
	// how long it keeps retrying to replace a file another process has open.
	restrictLockWait = 5 * time.Second
	replaceWait      = 5 * time.Second
)

// RestrictSystemLog puts the system log at path in the restricted access mode. The gid is ignored
// on Windows, where the readers group is a local group named by its SID.
//
// The directory gets the restricted ACL first (sysacl.SecureRestrictedLogDir): SYSTEM and
// Administrators, and read for the readers group, with nothing for anyone else. Then the marker
// that tells the collector and the CLI the mode is written there, inheriting that ACL.
//
// A log that was shared is not left with the ACL change alone. Any logged-on user could open it,
// its archives or its lock while INTERACTIVE held read and write data on them, and a handle keeps
// the access it was opened with: it could go on reading new events, overwriting old ones, or hold
// the lock. So each file is copied into a new file, which inherits the restricted ACL, and renamed
// over the old one under the log's lock, which leaves any such handle on a file nothing writes or
// reads any more. A rename over a file another process holds open without sharing delete fails; it
// is retried for a few seconds, and then the call fails and names the file, so install rolls back
// rather than reporting a restriction it could not make.
func RestrictSystemLog(path string, _ int) error {
	readers, err := logReadersSID()
	if err != nil {
		return fmt.Errorf("the log readers group: %w", err)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	wasRestricted := asymptoteobserve.RuntimeLogAccessRestricted(path)
	if err := sysacl.SecureRestrictedLogDir(dir, base, readers); err != nil {
		return fmt.Errorf("secure the log directory %s: %w", dir, err)
	}
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessRestricted); err != nil {
		return fmt.Errorf("record the restricted mode: %w", err)
	}
	lockPath := path + ".lock"
	if !wasRestricted {
		if err := replaceLogFamily(path); err != nil {
			return err
		}
	}
	// The lock and the log exist from here on, as a shared install leaves them: the lock narrowed
	// to SYSTEM and Administrators, the log with what it inherits.
	for _, name := range []string{lockPath, path} {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return err
		}
		_ = f.Close()
	}
	return sysacl.SecureRestrictedLock(lockPath)
}

// replaceLogFamily replaces the log and its archives with fresh copies while holding the log's lock
// (bounded), so no line lands in an old file after its copy was taken, and then the lock itself.
//
// The lock is replaced after it is released: a hook already waiting for it holds it open, which
// blocks the rename until that hook has taken and released it. No hook can open it afresh by
// then, because the directory's ACL no longer lets ordinary users open anything in it.
func replaceLogFamily(path string) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if sysacl.IsLogFamily(entry.Name(), base) && !strings.EqualFold(entry.Name(), base+".lock") {
			names = append(names, entry.Name())
		}
	}
	var errs []error
	func() {
		if lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0); err == nil {
			defer lock.Close()
			ctx, cancel := context.WithTimeout(context.Background(), restrictLockWait)
			held, err := filelock.ExclusiveContext(ctx, lock)
			cancel()
			if err == nil {
				defer held.Release()
			}
		}
		for _, name := range names {
			if err := replaceWithCopy(filepath.Join(dir, name)); err != nil {
				errs = append(errs, err)
			}
		}
	}()
	if err := replaceWithCopy(path + ".lock"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// replaceWithCopy copies the file at path into a new file beside it and renames that over it. A
// file that is missing, or is not a plain file, is left alone; SecureRestrictedLogDir has already
// removed any link where a log file belongs.
func replaceWithCopy(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".restrict-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	copyErr := copyInto(tmp, path)
	if err := tmp.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("copy %s: %w", path, copyErr)
	}
	deadline := time.Now().Add(replaceWait)
	for {
		err := os.Rename(tmpName, path)
		if err == nil {
			return nil
		}
		if !(errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			_ = os.Remove(tmpName)
			return fmt.Errorf("replace %s: %w; another process holds it open, so access granted while the log was shared could outlive the restriction -- close it and run again", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// copyInto copies the file at path into dst, closing its own handle before returning: Go opens
// files without sharing delete, so a handle left open would block the rename that follows.
func copyInto(dst *os.File, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}

// ShareSystemLog returns the system log at path to the shared access mode: the marker goes, and
// EnsureSystemLogWritable gives the directory and the log family the shared ACL again, the lock's
// narrower one included.
func ShareSystemLog(path string) error {
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessShared); err != nil {
		return fmt.Errorf("remove the restricted mode marker: %w", err)
	}
	return EnsureSystemLogWritable(path)
}
