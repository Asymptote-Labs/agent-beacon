//go:build !windows

package onboarding

import (
	"errors"
	"os"
	"syscall"
)

// geteuid is a seam: tests cannot run as root.
var geteuid = os.Geteuid

// keepOwner gives tmp the owner and group of the file at path, when root is replacing a file it
// does not own. Any other account can only replace files with ones it owns, as before.
func keepOwner(tmp *os.File, path string) error {
	if geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid == 0 && st.Gid == 0) {
		return nil
	}
	return tmp.Chown(int(st.Uid), int(st.Gid))
}
