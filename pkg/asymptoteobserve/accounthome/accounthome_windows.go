//go:build windows

package accounthome

import (
	"os"

	"golang.org/x/sys/windows"
)

// lookup reads the profile directory from the process token. USERPROFILE is the fallback when the
// token has no profile loaded.
func lookup() (string, error) {
	if dir, err := windows.GetCurrentProcessToken().GetUserProfileDirectory(); err == nil && dir != "" {
		return dir, nil
	}
	return os.UserHomeDir()
}
