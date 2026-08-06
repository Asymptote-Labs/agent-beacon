//go:build !windows

package endpoint

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity returns a stable identifier for the file behind an open handle.
//
// This is how the reader follows a rotated log. Rotation renames runtime.jsonl to runtime.jsonl.1,
// so the path a saved byte offset was recorded against now names different content -- resuming from
// that offset would skip or re-read events. Identity survives the rename, so the offset can follow
// the content instead of the name.
//
// Device and inode are that identity on POSIX. An empty result means "unknown", which callers
// already handle by falling back to path-based offsets.
func fileIdentity(_ *os.File, info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}
