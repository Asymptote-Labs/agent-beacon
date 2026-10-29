//go:build unix

package diagnostics

import (
	"os"
	"syscall"
)

func fileGroup(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Gid), true
}
