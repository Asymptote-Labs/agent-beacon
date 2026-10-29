//go:build !unix

package lifecycle

import "os"

// ownedByRoot is never true off Unix, where restricted log mode does not exist.
func ownedByRoot(os.FileInfo) bool { return false }
