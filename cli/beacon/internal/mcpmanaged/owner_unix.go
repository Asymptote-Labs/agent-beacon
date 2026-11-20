//go:build !windows

package mcpmanaged

import (
	"os"
	"syscall"
)

// fileOwner is the owner and group of an existing file.
func fileOwner(info os.FileInfo) *fileOwnerIDs {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &fileOwnerIDs{uid: int(st.Uid), gid: int(st.Gid)}
}
