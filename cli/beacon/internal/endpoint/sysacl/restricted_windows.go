//go:build windows

package sysacl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SecureRestrictedLogDir gives the system log directory the restricted mode's protected DACL and
// resets everything in it to match. dir is created if missing and replaced if it is a link. Below
// it, every link is removed, and so is every file of the log family that is hard-linked elsewhere;
// every other file and directory gets Administrators ownership and exactly what a new one there
// inherits, and the lock gets RestrictedLockDACL. A file outside the log family that is hard-linked
// elsewhere is left alone, since changing its security would change the other file's too.
//
// Everything, not only the log family: every account holds the right to bypass traverse checking,
// so a file that kept a grant from the shared mode -- the inventory log beside the runtime log, an
// operator's own file -- could still be opened by its full path once the directory itself is
// closed. In the shared mode files in this directory grant INTERACTIVE read and write, and that has
// to go with the mode.
//
// This changes what a new open is granted. A handle an account opened while the log was shared
// keeps the access it was opened with, so the caller also replaces each file of the family with a
// fresh copy (see writer.RestrictSystemLog).
func SecureRestrictedLogDir(dir, base, readersSID string) error {
	return withPrivileges(func() error {
		h, err := ensureRealDir(dir)
		if err != nil {
			return err
		}
		dacl := RestrictedLogDirDACL(readersSID)
		err = setSecurity(h, true, dacl)
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("secure %s: %w", dir, err)
		}
		return resetRestricted(dir, dacl, base)
	})
}

// resetRestricted gives everything below dir, which carries dacl, what it would inherit. base names
// the log family in the top directory and is empty below it.
func resetRestricted(dir string, dacl []ACE, base string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		family := base != "" && IsLogFamily(entry.Name(), base)
		h, info, err := openNoFollow(child)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("open %s: %w", child, err))
			continue
		}
		link := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		hardLinked := !isDir && info.NumberOfLinks > 1
		var childDACL []ACE
		switch {
		case link || (family && hardLinked):
			windows.CloseHandle(h)
			if err := os.Remove(child); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove the link %s: %w", child, err))
			}
			continue
		case hardLinked:
			windows.CloseHandle(h)
			continue
		case family && strings.EqualFold(entry.Name(), base+".lock"):
			err = setSecurity(h, true, RestrictedLockDACL())
		default:
			childDACL = InheritedDACL(dacl, isDir)
			err = setSecurity(h, false, childDACL)
		}
		windows.CloseHandle(h)
		if err != nil {
			errs = append(errs, fmt.Errorf("secure %s: %w", child, err))
			continue
		}
		if isDir {
			if err := resetRestricted(child, childDACL, ""); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// SecureRestrictedLock gives the lock at path RestrictedLockDACL, without following a link. The
// restricted mode replaces the lock with a fresh file, which inherits the directory's entries --
// the readers group's among them -- so it is narrowed again afterwards.
func SecureRestrictedLock(path string) error {
	return withPrivileges(func() error {
		h, info, err := openNoFollow(path)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks > 1 {
			return fmt.Errorf("%s is not a plain file", path)
		}
		return setSecurity(h, true, RestrictedLockDACL())
	})
}

// InspectRestrictedLogDir reports every way the directory, and anything directly in it, lets an
// account other than SYSTEM, Administrators and the readers group reach the log (see
// RestrictedProblems). It reads
// security only; what it cannot open -- an ordinary user cannot open anything there -- is reported
// as an error rather than as a problem.
func InspectRestrictedLogDir(dir, base, readersSID string) ([]string, error) {
	var problems []string
	obj, isLink, err := readObject(dir)
	if err != nil {
		return nil, err
	}
	if isLink {
		return []string{dir + " is a link, so writes to it land wherever it points"}, nil
	}
	if !obj.Protected {
		problems = append(problems, dir+": inherits entries from its parent")
	}
	for _, p := range RestrictedProblems(obj, readersSID, false) {
		problems = append(problems, dir+": "+p)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return problems, err
	}
	// Every entry, not only the log family, for the reason SecureRestrictedLogDir resets them all.
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		fobj, link, err := readObject(child)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return problems, err
		}
		if link {
			problems = append(problems, child+" is a link, so writes to it land wherever it points")
			continue
		}
		lock := strings.EqualFold(entry.Name(), base+".lock")
		for _, p := range RestrictedProblems(fobj, readersSID, lock) {
			problems = append(problems, child+": "+p)
		}
	}
	return problems, nil
}

// CreateRestrictedFile creates a new file at path in the restricted log directory, failing if
// anything is there. Its security is set by the creation itself, so there is no moment in which it
// carries anything else: owned by Administrators whoever the creator is (a policy can make an
// administrator's files their own), with RestrictedLockDACL when lock is set and otherwise exactly
// what the directory passes on.
func CreateRestrictedFile(path string, lock bool) (*os.File, error) {
	sddl := "O:" + sidAdministrators
	if lock {
		sddl = SDDL(sidAdministrators, true, RestrictedLockDACL())
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build the security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// ResetRestrictedFile gives the plain file at path Administrators ownership and exactly what a new
// file in the restricted log directory inherits, without following a link.
func ResetRestrictedFile(path, readersSID string) error {
	return withPrivileges(func() error {
		h, info, err := openNoFollow(path)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks > 1 {
			return fmt.Errorf("%s is not a plain file", path)
		}
		return setSecurity(h, false, InheritedDACL(RestrictedLogDirDACL(readersSID), false))
	})
}
