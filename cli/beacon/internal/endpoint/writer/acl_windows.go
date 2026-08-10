//go:build windows

package writer

import (
	"fmt"
	"os/exec"
	"strings"
)

// grantInteractiveUsersWrite makes a machine-wide log directory writable by ordinary users.
//
// This is the Windows counterpart to the 0666 runtime log on POSIX, and it exists for the same
// reason: a system-mode collector runs elevated and creates the log, while hooks run as the person
// at the keyboard and append to it. On POSIX the file mode carries that. Windows has no mode --
// %ProgramData% subdirectories inherit an ACL where only administrators and SYSTEM may write, so
// without an explicit grant every hook write fails with access denied.
//
// The failure that makes it worth doing at install time is the quiet one. Nothing errors visibly:
// the collector is healthy, `endpoint status` reports running, and `doctor` agrees -- because all
// of them describe the collector, not the hooks that export to it. The log simply stays empty of
// anything the agent did. That is the same shape as the Linux SUDO_USER bug, where a package
// install produced a perfectly healthy collector that captured nothing.
//
// Granted to INTERACTIVE rather than Users or Everyone. INTERACTIVE covers anyone logged on at the
// machine, which is exactly the set whose agent sessions this endpoint is meant to capture, and
// excludes service and network logons that have no business writing here.
//
// (OI)(CI) makes the grant inherit to files and subdirectories created later, so the log and its
// rotated archives are covered without re-running this after every rotation.
func grantInteractiveUsersWrite(dir string) error {
	// icacls rather than the security APIs directly: it is present on every supported Windows,
	// its argument form is stable, and the equivalent through SetNamedSecurityInfo means building
	// an ACL by hand for a grant an administrator can read and audit in one line.
	cmd := exec.Command("icacls", dir, "/grant", `*S-1-5-4:(OI)(CI)(M)`, "/T")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant interactive users write access to %s: %w: %s",
			dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// interactiveUsersCanWrite reports whether the grant is in place, for doctor.
//
// Checked by reading the ACL rather than by attempting a write: doctor runs elevated, so a test
// write would succeed regardless and report healthy for exactly the configuration that is broken.
// That is the trap this check exists to avoid, so it must not fall into it.
func interactiveUsersCanWrite(dir string) (bool, error) {
	out, err := exec.Command("icacls", dir).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("read the ACL on %s: %w: %s", dir, err, strings.TrimSpace(string(out)))
	}
	text := string(out)
	// icacls renders the well-known SID as a localized account name, so the SID itself is not
	// reliably present in the output. Both spellings are accepted: the localized name varies by
	// system language, and matching only the English one would report a correct grant as missing
	// on a German or Japanese install.
	for _, marker := range []string{"S-1-5-4", "INTERACTIVE"} {
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(strings.ToUpper(line), strings.ToUpper(marker)) {
				continue
			}
			// (M) is modify, (F) is full control; either is enough to append.
			if strings.Contains(line, "(M)") || strings.Contains(line, "(F)") || strings.Contains(line, "(W)") {
				return true, nil
			}
		}
	}
	return false, nil
}
