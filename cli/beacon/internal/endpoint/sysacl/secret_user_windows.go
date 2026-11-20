//go:build windows

package sysacl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// currentUserSID is the account this process runs as.
func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read this account's SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

// WriteUserSecretFile replaces path with data, as a file only this account and SYSTEM can open.
//
// The temp file is created with UserSecretFileDACL by CreateFile itself, so it never carries what
// the directory passes on, and the rename keeps that descriptor. It needs no privilege: the owner is
// the creating account.
func WriteUserSecretFile(path string, data []byte) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(daclSDDL(true, UserSecretFileDACL(sid)))
	if err != nil {
		return fmt.Errorf("build the security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	var tmp string
	var h windows.Handle
	for attempt := 0; ; attempt++ {
		tmp = filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), attempt))
		p, err := windows.UTF16PtrFromString(tmp)
		if err != nil {
			return err
		}
		h, err = windows.CreateFile(p, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			break
		}
		if err != windows.ERROR_FILE_EXISTS || attempt >= 16 {
			runtime.KeepAlive(sd)
			return &os.PathError{Op: "create", Path: tmp, Err: err}
		}
	}
	runtime.KeepAlive(sd)
	f := os.NewFile(uintptr(h), tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// SecureUserSecretDir gives dir the protected UserSecretDirDACL for this account and resets
// everything already below it to what it inherits from that, so nothing in it stays readable by
// other accounts and everything written there later inherits the same two entries. dir is created
// if missing and replaced if it is a link; links below it are removed. Owners are left as they are,
// which is what lets an ordinary account run it on its own profile.
func SecureUserSecretDir(dir string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	dacl := UserSecretDirDACL(sid)
	h, err := ensureRealDir(dir)
	if err != nil {
		return err
	}
	err = setDACL(h, true, dacl)
	windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("secure %s: %w", dir, err)
	}
	return resetUserPrivate(dir, dacl)
}

// setDACL replaces only the DACL on h.
func setDACL(h windows.Handle, protected bool, dacl []ACE) error {
	sd, err := windows.SecurityDescriptorFromString(daclSDDL(protected, dacl))
	if err != nil {
		return fmt.Errorf("build the security descriptor: %w", err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protected {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	err = windows.SetKernelObjectSecurity(h, info, sd)
	runtime.KeepAlive(sd)
	return err
}

// resetUserPrivate gives everything below dir, which carries dacl, what it would inherit. A link is
// removed rather than followed; a file hard-linked elsewhere is left alone, since changing its
// security would change the other name's too.
func resetUserPrivate(dir string, dacl []ACE) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		h, info, err := openNoFollow(child)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("open %s: %w", child, err))
			continue
		}
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			windows.CloseHandle(h)
			if err := os.Remove(child); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove the link %s: %w", child, err))
			}
			continue
		}
		if !isDir && info.NumberOfLinks > 1 {
			windows.CloseHandle(h)
			continue
		}
		childDACL := InheritedDACL(dacl, isDir)
		err = setDACL(h, false, childDACL)
		windows.CloseHandle(h)
		if err != nil {
			errs = append(errs, fmt.Errorf("secure %s: %w", child, err))
			continue
		}
		if isDir {
			if err := resetUserPrivate(child, childDACL); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
