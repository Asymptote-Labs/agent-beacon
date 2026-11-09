//go:build !windows

package accounthome

import (
	"os"
	"os/user"
	"strings"
)

// lookup reads the home directory from the account database (the passwd entry). Beacon builds
// without cgo, so on Linux Go reads /etc/passwd only: an account that is listed only in a directory
// service (LDAP, SSSD) or not at all falls back to $HOME, as does any lookup failure.
func lookup() (string, error) {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.HomeDir) != "" {
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}
