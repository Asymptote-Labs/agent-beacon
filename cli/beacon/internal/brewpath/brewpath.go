// Package brewpath maps a path inside a Homebrew keg to the link that survives upgrades.
package brewpath

import (
	"os"
	"path/filepath"
	"strings"
)

// Stable returns <prefix>/bin/<name> for a path inside a Homebrew Cellar keg when that link
// exists, and path unchanged otherwise.
//
// Homebrew installs each version under <prefix>/Cellar/<formula>/<version> and links its binaries
// into <prefix>/bin. os.Executable reports the keg on Linux, where it reads /proc/self/exe, and a
// service unit or search result built from that path breaks at the next `brew upgrade`, which
// deletes the old keg. The link keeps resolving to whichever version is current.
func Stable(path string) string {
	idx := strings.Index(path, string(filepath.Separator)+"Cellar"+string(filepath.Separator))
	if idx <= 0 {
		return path
	}
	linked := filepath.Join(path[:idx], "bin", filepath.Base(path))
	if _, err := os.Stat(linked); err != nil {
		return path
	}
	return linked
}
