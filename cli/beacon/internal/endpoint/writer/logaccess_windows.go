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
	"unsafe"

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
// Administrators, and read for the readers group, with nothing for anyone else, and everything in it
// is reset to match.
//
// A log that was shared is not left with the ACL change alone. Any logged-on user could open it,
// its archives or its lock while INTERACTIVE held read and write data on them, and a handle keeps
// the access it was opened with: it could go on reading new events, overwriting old ones, or hold
// the lock. So each file is copied into a new file and renamed over the old one under the log's
// lock, which leaves any such handle on a file nothing writes or reads any more. Each copy is
// created with its final owner and DACL (sysacl.CreateRestrictedFile), so not even the readers
// group can open the new lock in between. A file another process holds open without sharing delete
// cannot be replaced; that is retried for a few seconds, and then the call fails and names the file,
// so install rolls back rather than reporting a restriction it could not make.
//
// The marker that records the mode is written last, once every file has been replaced. On Windows
// it does not change how the root writers open the log -- the directory's ACL decides what a new
// file grants -- so nothing needs it earlier, and writing it last means a run that failed or was
// cut short is not mistaken for a finished one: the next run copies the files again.
func RestrictSystemLog(path string, _ int) error {
	readers, err := logReadersSID()
	if err != nil {
		return fmt.Errorf("the log readers group: %w", err)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	wasRestricted := asymptoteobserve.RuntimeLogAccessRestricted(path)
	if err := sysacl.SecureRestrictedLogDir(dir, base, readers); err != nil {
		return rollBack(path, wasRestricted, fmt.Errorf("secure the log directory %s: %w", dir, err))
	}
	if err := finishRestriction(path, readers, wasRestricted); err != nil {
		return rollBack(path, wasRestricted, err)
	}
	return nil
}

// rollBack puts a log that was shared before a restriction that failed back to shared, so the
// failure leaves hooks a directory they can still append to. The restricted marker is written
// last, so nothing that reads it (install's own rollback included) would know the directory had
// already been narrowed.
func rollBack(path string, wasRestricted bool, err error) error {
	if wasRestricted {
		return err
	}
	if shareErr := ShareSystemLog(path); shareErr != nil {
		return errors.Join(err, fmt.Errorf("restore the shared log after the failure: %w", shareErr))
	}
	return err
}

func finishRestriction(path, readers string, wasRestricted bool) error {
	if !wasRestricted {
		if err := replaceLogFamily(path); err != nil {
			return err
		}
	}
	// The lock and the log exist from here on, as a shared install leaves them.
	for name, lock := range map[string]bool{path + ".lock": true, path: false} {
		f, err := sysacl.CreateRestrictedFile(name, lock)
		if err == nil {
			_ = f.Close()
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessRestricted); err != nil {
		return fmt.Errorf("record the restricted mode: %w", err)
	}
	// The marker counts only when an administrator owns it, which the creator's default owner may
	// not be.
	if err := sysacl.ResetRestrictedFile(asymptoteobserve.LogAccessMarkerPath(path), readers); err != nil {
		return fmt.Errorf("secure the restricted mode marker: %w", err)
	}
	return nil
}

// replaceLogFamily replaces the log, its archives and the other files beside it with fresh copies while holding the log's lock
// (bounded), so no line lands in an old file after its copy was taken, and then the lock itself.
// The family is listed once the lock is held: the collector rotates under it, so a list taken
// before could miss the archive a rotation made while this waited.
//
// The lock is replaced after it is released: a hook already waiting for it holds it open, which
// blocks the rename until that hook has taken and released it. No hook can open it afresh by
// then, because the directory's ACL no longer lets ordinary users open anything in it.
func replaceLogFamily(path string) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			return
		}
		// Every file directly in the directory, not only the log family: the shared ACL reached
		// all of them (inventory_state.jsonl, the self-updater's system.jsonl), so any could be
		// held open by an account the restriction shuts out. Their own writers do not take this
		// lock, so a line one of them appends between the copy and the rename is lost; that is
		// rare, and the price of not leaving such a handle in place. A file outside the family
		// with another hard link is left alone, as SecureRestrictedLogDir leaves it: the other
		// link could be any file, and a copy would hand its content to the readers group.
		for _, entry := range entries {
			name := entry.Name()
			if strings.EqualFold(name, base+".lock") || strings.Contains(name, ".restrict-") {
				continue
			}
			child := filepath.Join(dir, name)
			if !sysacl.IsLogFamily(name, base) && hardLinked(child) {
				continue
			}
			if err := replaceWithCopy(child, false); err != nil {
				errs = append(errs, err)
			}
		}
	}()
	if err := replaceWithCopy(path+".lock", true); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// hardLinked reports whether the file at path has more than one name. A file it cannot open counts
// as linked, so nothing unknown is copied.
func hardLinked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return true
	}
	return info.NumberOfLinks > 1
}

// replaceWithCopy copies the file at path into a new file beside it, created with its final
// security (the lock's when lock is set), and renames that over it. A file that is missing, or is
// not a plain file, is left alone; SecureRestrictedLogDir has already removed any link where a log
// file belongs.
func replaceWithCopy(path string, lock bool) error {
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
	tmpName := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.restrict-%d", filepath.Base(path), time.Now().UnixNano()))
	tmp, err := sysacl.CreateRestrictedFile(tmpName, lock)
	if err != nil {
		return err
	}
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
		err := renameReplacing(tmpName, path)
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

// fileRenameInfo is FILE_RENAME_INFO with its Flags member (FileRenameInfoEx).
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// renameReplacing renames from over to with POSIX semantics. MoveFileEx, which os.Rename uses,
// refuses to replace a file that is open at all, even by a process that allowed others to delete
// it; with POSIX semantics such a file is replaced and its handles stay on the old file, which is
// exactly what cuts off a handle opened while the log was shared. A file open without sharing
// delete still cannot be replaced, and that error is returned. File systems without POSIX renames
// fall back to os.Rename.
func renameReplacing(from, to string) error {
	p, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(h)
		}
	}()
	name, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	size := int(unsafe.Offsetof(fileRenameInfo{}.FileName)) + len(name)*2
	buf := make([]uint64, (size+7)/8) // FILE_RENAME_INFO needs the alignment of its handle
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	err = windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(&buf[0])), uint32(size))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		windows.CloseHandle(h)
		closed = true
		return os.Rename(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
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
	// Read before the marker goes: whether the directory was restricted, by the marker or, for a
	// restriction cut short before writing it, by the directory's own ACL.
	wasRestricted := asymptoteobserve.RuntimeLogAccessRestricted(path) || sysacl.LooksRestricted(filepath.Dir(path))
	if err := asymptoteobserve.SetRuntimeLogAccess(path, asymptoteobserve.LogAccessShared); err != nil {
		return fmt.Errorf("remove the restricted mode marker: %w", err)
	}
	if err := EnsureSystemLogWritable(path); err != nil {
		return err
	}
	if wasRestricted {
		// EnsureSystemLogWritable resets only the log family; the restricted mode reset every file
		// in the directory, and they go back too.
		if err := sysacl.ResetLogDirToShared(filepath.Dir(path)); err != nil {
			return fmt.Errorf("give the log directory back its shared grants: %w", err)
		}
	}
	return nil
}

// CheckRestrictedSystemLogACL lists every way the restricted log at path, its directory, its
// archives or its lock let an account other than SYSTEM, Administrators and the readers group
// reach it (sysacl.RestrictedProblems), for doctor. An ordinary user cannot open the directory, and
// gets that error back.
func CheckRestrictedSystemLogACL(path string) ([]string, error) {
	readers, err := logReadersSID()
	if err != nil {
		return nil, err
	}
	return sysacl.InspectRestrictedLogDir(filepath.Dir(path), filepath.Base(path), readers)
}
