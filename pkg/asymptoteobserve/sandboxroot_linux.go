//go:build linux

package asymptoteobserve

import (
	"os"
	"path/filepath"
	"syscall"
)

// sandboxRootDir is the root directory OnSandboxRoot inspects; a variable so tests can stand a
// directory they own in for it.
var sandboxRootDir = "/"

// tmpfsMagic is TMPFS_MAGIC, the f_type statfs reports for a tmpfs.
const tmpfsMagic = 0x01021994

// filesystemIsTmpfs reports whether path lies on a tmpfs; a variable so tests can stand any
// directory in for bubblewrap's root.
var filesystemIsTmpfs = func(path string) bool {
	var fs syscall.Statfs_t
	return syscall.Statfs(path, &fs) == nil && int64(fs.Type) == tmpfsMagic
}

// OnSandboxRoot reports whether path, or the nearest directory above it that exists, lies on the
// throwaway root filesystem of a sandbox this account assembled itself: a tmpfs mounted at / that
// the account owns. Bubblewrap builds exactly that, with or without a user namespace, where a real
// host's / is root's and a persistent one (a rootfs directory bound at /, an Apptainer sandbox) is
// not a tmpfs. Anything created on it vanishes with the sandbox, so a writer that would put a log
// or a spool there reports the event as not recorded instead. What the sandbox bind-mounts (a
// workspace, a home, Beacon's own directories) is another filesystem and stays real. Root's own
// writes never count: root needs no sandbox root of its own to write anywhere.
func OnSandboxRoot(path string) bool {
	euid := os.Geteuid()
	if euid == 0 {
		return false
	}
	var root syscall.Stat_t
	if err := syscall.Stat(sandboxRootDir, &root); err != nil || int(root.Uid) != euid || !filesystemIsTmpfs(sandboxRootDir) {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for dir := abs; ; {
		var st syscall.Stat_t
		if err := syscall.Stat(dir, &st); err == nil {
			return st.Dev == root.Dev
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}
		dir = parent
	}
}
