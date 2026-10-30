//go:build windows

package localingest

import "golang.org/x/sys/windows"

// processPrivileged reports whether this process runs as SYSTEM or with an elevated token: either
// may write the restricted log, whose ACL gives SYSTEM and Administrators full control, and neither
// may be stamped as anyone else.
func processPrivileged() bool {
	token := windows.GetCurrentProcessToken()
	if token.IsElevated() {
		return true
	}
	user, err := token.GetTokenUser()
	return err == nil && user.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}
