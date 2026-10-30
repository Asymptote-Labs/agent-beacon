//go:build unix

package localingest

import "os"

// processPrivileged reports whether this process is root.
func processPrivileged() bool { return os.Geteuid() == 0 }

// accountHome is the home directory the spool lives under.
func accountHome() (string, error) { return os.UserHomeDir() }
