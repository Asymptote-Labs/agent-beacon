//go:build windows

package otlphttpreceiver

import (
	"testing"

	"golang.org/x/sys/windows"
)

// currentOwner is how peercred names this process's account: the SID of its token's user.
func currentOwner(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}
