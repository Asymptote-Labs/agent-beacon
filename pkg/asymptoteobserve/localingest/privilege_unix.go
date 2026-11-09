//go:build unix

package localingest

import "os"

// processPrivileged reports whether this process is root.
func processPrivileged() bool { return os.Geteuid() == 0 }
