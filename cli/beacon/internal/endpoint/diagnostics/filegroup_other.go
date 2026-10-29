//go:build !unix

package diagnostics

import "os"

func fileGroup(os.FileInfo) (int, bool) { return 0, false }
