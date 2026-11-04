//go:build windows

package loggroup

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// On Windows the readers group is a local group. It has a SID rather than a gid: lookup reports
// gid 0 when the group exists, and SID gives what the log directory's ACL names.

// lookupLocal resolves name on this computer only. A bare name would also match a domain group of
// the same name on a domain-joined machine, which a domain administrator rather than this machine's
// controls, so the name is qualified with the computer's own and must resolve to a local group.
var lookupLocal = func(name string) (sid string, found bool, err error) {
	computer, err := windows.ComputerName()
	if err != nil {
		return "", false, fmt.Errorf("read the computer name: %w", err)
	}
	s, _, accType, err := windows.LookupSID("", computer+`\`+name)
	if err != nil {
		if errors.Is(err, windows.ERROR_NONE_MAPPED) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("look up %s: %w", name, err)
	}
	if accType != windows.SidTypeAlias {
		return "", false, fmt.Errorf("%s exists on this computer but is not a local group", name)
	}
	return s.String(), true, nil
}

func lookup(name string) (int, bool, error) {
	_, found, err := lookupLocal(name)
	return 0, found, err
}

// SID returns the local group's SID and whether it exists.
func SID(name string) (string, bool, error) {
	if err := validName(name); err != nil {
		return "", false, err
	}
	return lookupLocal(name)
}

// create adds the local group with net.exe from the system directory, never one found on PATH.
func create(name string) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("find the system directory: %w", err)
	}
	_, err = run(filepath.Join(system, "net.exe"), "localgroup", name, "/add",
		"/comment:Accounts that may read the Beacon system runtime log")
	return err
}
