//go:build windows

package localingest

import "golang.org/x/sys/windows"

// processPrivileged reports whether this process may write the restricted log itself: it runs as
// SYSTEM, or its token has Administrators enabled. Those are the accounts the log's ACL gives
// write, and neither may be stamped as anyone else. Elevation alone is not enough: a token elevated
// for another group (Backup Operators run as administrator, say) is still refused by the ACL, so it
// must go through the pipe and the spool like any other account.
func processPrivileged() bool {
	token := windows.GetCurrentProcessToken()
	if user, err := token.GetTokenUser(); err == nil && user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return true
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	// A zero token asks CheckTokenMembership about this thread's token, or a copy of the process's,
	// counting only enabled groups: an administrator's filtered token holds Administrators for deny
	// only, so a shell that is not elevated is not privileged.
	member, err := windows.Token(0).IsMember(admins)
	return err == nil && member
}
