//go:build windows

package sessionfs

import "io/fs"

const openNonblock = 0

func ownerUID(fs.FileInfo) (int, bool) { return 0, false }

// OwnerUID reports -1 on Windows, where file ownership is not a uid; a confinement with a negative
// uid checks containment and file type but not ownership.
func OwnerUID(string) (int, error) { return -1, nil }
