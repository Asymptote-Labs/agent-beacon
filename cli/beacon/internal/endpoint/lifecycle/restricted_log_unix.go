//go:build unix

package lifecycle

import (
	"os"
	"syscall"
)

func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
