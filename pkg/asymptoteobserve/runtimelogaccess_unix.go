//go:build unix

package asymptoteobserve

import (
	"io"
	"os"
	"strings"
	"syscall"
)

// readLogAccessMarker returns the mode a trusted marker records, or "" when there is no marker or
// it could have been written by someone other than root: not a regular file, owned by another
// account, writable by group or others, or with another hard link.
func readLogAccessMarker(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != logAccessMarkerOwner || stat.Nlink != 1 {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLogAccessMarkerBytes+1))
	if err != nil || len(data) > maxLogAccessMarkerBytes {
		return ""
	}
	return strings.TrimSpace(string(data))
}
