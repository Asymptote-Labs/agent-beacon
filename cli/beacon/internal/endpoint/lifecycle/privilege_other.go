//go:build !windows

package lifecycle

import "os"

// HasSystemPrivileges reports whether this process may write machine-wide state.
//
// Root, which is what /etc, /Library and /var/log all require and what launchd and systemd require to
// register a system service.
func HasSystemPrivileges() bool {
	return os.Geteuid() == 0
}

func SystemPrivilegeHint() string {
	return "rerun with sudo"
}
