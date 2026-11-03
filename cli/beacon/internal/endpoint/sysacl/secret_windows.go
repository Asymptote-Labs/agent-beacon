//go:build windows

package sysacl

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WriteSecretFile replaces path with data, as a file only SYSTEM and Administrators can open.
//
// The temp file is created with SecretFileDACL and Administrators ownership by CreateFile itself, so
// there is no moment in which it carries what the directory passes on, and the rename keeps that
// descriptor. Like atomicfile.Write, a reader never sees a half-written file.
func WriteSecretFile(path string, data []byte) error {
	sd, err := windows.SecurityDescriptorFromString(SDDL(sidAdministrators, true, SecretFileDACL()))
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

// IsSecretFile reports whether path carries SecretFileDACL, so a rollback that puts a snapshot back
// can restore it the same way rather than with the tree's DACL.
func IsSecretFile(path string) bool {
	obj, err := ReadObject(path)
	return err == nil && obj.Protected && reflect.DeepEqual(obj.DACL, SecretFileDACL())
}

// SecureSecretDir gives dir Administrators ownership and the protected SecretDirDACL, and resets
// everything already below it to what it inherits from that, so nothing in it stays readable by
// other accounts and everything written there later inherits the same two entries. dir is created
// if missing and replaced if it is a link; links below it are removed. A directory that is already
// private passes SecureSystemTree's checks, so a later pass over the tree leaves it alone.
func SecureSecretDir(dir string) error {
	return withPrivileges(func() error {
		h, err := ensureRealDir(dir)
		if err != nil {
			return err
		}
		err = setSecurity(h, true, SecretDirDACL())
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("secure %s: %w", dir, err)
		}
		return resetRestricted(dir, SecretDirDACL(), "")
	})
}
