//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/osuser"
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
// The account database first, since that is the authoritative answer, and osuser consults NSS so
// a directory-backed account resolves rather than falling straight through. Falling back to the
// home directory's owner still covers what NSS cannot answer -- a container with a bare passwd
// file, or a host with no getent -- where the directory itself still records who they are.
//
// Both this and resolveConsoleUser go through osuser deliberately. They answer the same question
// and disagreeing about it is how a settings file ends up owned by someone who cannot read it.
func userOwnership(info consoleUserInfo) (int, int, error) {
	if u, err := osuser.Lookup(info.Username); err == nil {
		return u.UID, u.GID, nil
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
