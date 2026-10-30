//go:build windows

package sysacl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// SecureRestrictedLogDir gives the system log directory the restricted mode's protected DACL and
// resets its log family to match, the way SecureLogDir does for the shared mode: dir is created if
// missing and replaced if it is a link; every link among its direct children, and every file of the
// log family hard-linked elsewhere, is removed; every other file of the family gets Administrators
// ownership and exactly what a new file inherits, and the lock gets RestrictedLockDACL. Anything
// else in the directory keeps its own security.
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
		err = setSecurity(h, true, RestrictedLogDirDACL(readersSID))
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("secure %s: %w", dir, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		inherited := InheritedDACL(RestrictedLogDirDACL(readersSID), false)
		var errs []error
		for _, entry := range entries {
			child := filepath.Join(dir, entry.Name())
			family := IsLogFamily(entry.Name(), base)
			h, info, err := openNoFollow(child)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("open %s: %w", child, err))
				continue
			}
			link := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
			dirEntry := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
			hardLinked := !dirEntry && info.NumberOfLinks > 1
			switch {
			case link || (family && hardLinked):
				windows.CloseHandle(h)
				if err := os.Remove(child); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, fmt.Errorf("remove the link %s: %w", child, err))
				}
			case family && !dirEntry && strings.EqualFold(entry.Name(), base+".lock"):
				if err := setSecurity(h, true, RestrictedLockDACL()); err != nil {
					errs = append(errs, fmt.Errorf("secure %s: %w", child, err))
				}
				windows.CloseHandle(h)
			case family && !dirEntry:
				if err := setSecurity(h, false, inherited); err != nil {
					errs = append(errs, fmt.Errorf("secure %s: %w", child, err))
				}
				windows.CloseHandle(h)
			default:
				windows.CloseHandle(h)
			}
		}
		return errors.Join(errs...)
	})
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

// InspectRestrictedLogDir reports every way the directory and its log family let an account other
// than SYSTEM, Administrators and the readers group reach the log (see RestrictedProblems). It reads
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
	for _, entry := range entries {
		if !IsLogFamily(entry.Name(), base) {
			continue
		}
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
