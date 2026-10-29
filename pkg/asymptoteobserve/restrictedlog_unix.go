//go:build unix

package asymptoteobserve

import (
	"os"
	"syscall"
)

// markerOwnedByRoot accepts a restricted-mode marker only when root owns it and nobody else can
// write it, so an ordinary user cannot switch a machine's hooks off the log by planting one.
func markerOwnedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && info.Mode().Perm()&0o022 == 0
}

// settleRestrictedLogGroup gives a file in a restricted log's directory the reader group. Only
// root can, and only root writes a restricted log; anyone else leaves the group alone.
func settleRestrictedLogGroup(info os.FileInfo, gid int, chown func(uid, gid int) error) error {
	if !runningAsRoot() {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid == 0 && int(stat.Gid) == gid) {
		return nil
	}
	return chown(0, gid)
}
