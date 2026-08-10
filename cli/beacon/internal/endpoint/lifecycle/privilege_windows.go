//go:build windows

package lifecycle

import "golang.org/x/sys/windows"

// HasSystemPrivileges reports whether this process may write machine-wide state.
//
// os.Geteuid returns -1 on Windows, unconditionally, so the POSIX check it replaces here rejected
// every system-mode install on the platform -- including from a fully elevated administrator shell.
// A dispatched run is what surfaced it: `beacon endpoint install --system` failed with "system install
// requires root", which reads like a mistake by the operator rather than a check that cannot pass.
//
// Elevation rather than group membership. An administrator's *unelevated* token cannot write to
// %ProgramData%\Beacon or open the Service Control Manager, so asking whether the account is in the
// Administrators group would report yes and then fail at the first write -- halfway through an install.
// IsElevated asks whether this process actually holds those rights now, which is the question.
func HasSystemPrivileges() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// SystemPrivilegeHint is the platform's version of "rerun with sudo".
func SystemPrivilegeHint() string {
	return "run this from an elevated shell (right-click the terminal and choose Run as administrator)"
}
