//go:build !windows

package collector

import "os"

// isExecutableMode reports whether the file's permission bits allow execution.
func isExecutableMode(info os.FileInfo) bool {
	return info.Mode().Perm()&0111 != 0
}
