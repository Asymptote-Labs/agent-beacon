//go:build windows

package sysacl

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// CreatePrivateFile creates a new file at path, failing if anything is there, owned by
// Administrators with the protected PrivateDACL. Its security is set by the creation itself, so
// nothing written to it is ever readable by an ordinary user, and a later rename within the volume
// keeps it.
func CreatePrivateFile(path string) (*os.File, error) {
	return createNew(path, SDDL(sidAdministrators, true, PrivateDACL(false)))
}

// SecurePrivateDir gives dir Administrators ownership and the protected PrivateDACL, and resets
// everything below it to what it inherits from that. dir is created if missing and replaced if it
// is a link; links below it are removed, as in the restricted log directory.
func SecurePrivateDir(dir string) error {
	return withPrivileges(func() error {
		h, err := ensureRealDir(dir)
		if err != nil {
			return err
		}
		dacl := PrivateDACL(true)
		err = setSecurity(h, true, dacl)
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("secure %s: %w", dir, err)
		}
		return resetRestricted(dir, dacl, "")
	})
}

// InspectPrivateFile reports every way path lets an account other than SYSTEM and Administrators
// reach it (see PrivateProblems). A link is a problem in itself. It reads security only.
func InspectPrivateFile(path string) ([]string, error) {
	obj, link, err := readObject(path)
	if err != nil {
		return nil, err
	}
	if link {
		return []string{path + " is a link"}, nil
	}
	return PrivateProblems(obj), nil
}

// IsPrivateFile reports whether path is a plain file only SYSTEM and Administrators may reach. A
// rewrite that keeps such a file's content keeps it private too (see atomicfile.WriteSystemPrivate).
func IsPrivateFile(path string) bool {
	problems, err := InspectPrivateFile(path)
	return err == nil && len(problems) == 0
}
