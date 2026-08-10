//go:build windows

package collector

import "os"

// isExecutableMode reports whether the file can be executed.
//
// Windows has no executable permission bits. Go reports 0666 for an ordinary file and 0444 for a
// read-only one, so the POSIX test `Perm()&0111 != 0` is false for *every* file there -- including
// a genuine .exe. Applied unchanged, it made DiscoverBinary reject every collector binary on
// Windows and report that none was installed.
//
// Executability there is decided by extension, and the caller is looking for one specific named
// binary rather than scanning for anything runnable, so being a regular file is the meaningful
// check: exec.LookPath already applies PATHEXT when resolving BinaryName from PATH, and the
// explicit candidate paths name the file directly.
func isExecutableMode(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
