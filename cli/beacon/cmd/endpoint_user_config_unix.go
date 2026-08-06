//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// chownUserConfigArtifacts gives the console user ownership of the settings files a system-mode
// install just wrote on their behalf.
//
// Load-bearing rather than cosmetic. The install runs as root, so every file it creates is
// root-owned; the person whose agent sessions matter then cannot edit their own settings, and their
// agent may not be able to write alongside them. This is the step that hands them back.
func chownUserConfigArtifacts(info consoleUserInfo, path string) error {
	uid, gid, err := userOwnership(info)
	if err != nil {
		return err
	}
	for _, candidate := range userConfigArtifacts(path) {
		if err := os.Chown(candidate, uid, gid); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// userOwnership resolves the console user's numeric uid and gid.
//
// The user database first, since that is the authoritative answer. Falling back to the home
// directory's owner covers a user whose account is not in the local database -- LDAP, SSSD, a
// container with a bare passwd file -- where the directory itself still records who they are.
func userOwnership(info consoleUserInfo) (int, int, error) {
	if u, err := user.Lookup(info.Username); err == nil {
		uid, uidErr := strconv.Atoi(u.Uid)
		gid, gidErr := strconv.Atoi(u.Gid)
		if uidErr == nil && gidErr == nil {
			return uid, gid, nil
		}
	}
	stat, err := os.Stat(info.HomeDir)
	if err != nil {
		return 0, 0, err
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("could not determine ownership for %s", info.HomeDir)
	}
	return int(sys.Uid), int(sys.Gid), nil
}
