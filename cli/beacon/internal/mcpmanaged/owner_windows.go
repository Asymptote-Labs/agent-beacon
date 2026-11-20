package mcpmanaged

import "os"

// fileOwner is nil on Windows, where managed servers are not pushed.
func fileOwner(os.FileInfo) *fileOwnerIDs { return nil }
