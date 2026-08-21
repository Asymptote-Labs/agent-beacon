//go:build windows

package writer

import (
	"fmt"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
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
	out, err := exec.Command("icacls", grantArgs(dir)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant interactive users write access to %s: %w: %s",
			dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// grantArgs is the single definition of the grant.
//
// icacls rather than the security APIs directly: it is present on every supported Windows, its
// argument form is stable, and the equivalent through SetNamedSecurityInfo means building an ACL by
// hand for a grant an administrator can read and audit in one line.
//
// The `*` prefix passes the well-known SID literally, so this does not depend on the machine's
// display language the way an `INTERACTIVE` or `INTERAKTIV` spelling would.
//
// One definition because doctor prints this command as the remediation for a missing grant. Advice
// that has drifted from the implementation is worse than no advice: an operator runs it, doctor
// still reports the failure, and the next thing they doubt is the diagnosis rather than the hint.
func grantArgs(dir string) []string {
	return []string{dir, "/grant", `*S-1-5-4:(OI)(CI)(M)`, "/T"}
}

// GrantCommandHint renders the grant as a command an operator can paste.
//
// Empty on platforms with no such grant, which is how callers tell that this remediation does not
// apply rather than printing a Windows command on a Mac.
//
// Quoting matters here in a way it does not for the grant itself: applying it goes through argv
// with no shell involved, while this is read by a person and pasted into whichever shell they have
// open. The rights string carries parentheses, which PowerShell reads as grouping, so an unquoted
// hint is a command that fails on paste -- and a remediation that fails on paste is worse than
// none, because the operator concludes the diagnosis was wrong.
func GrantCommandHint(dir string) string {
	parts := make([]string, 0, len(grantArgs(dir))+1)
	parts = append(parts, "icacls")
	for _, arg := range grantArgs(dir) {
		parts = append(parts, quoteForShellPaste(arg))
	}
	return strings.Join(parts, " ")
}

// quoteForShellPaste wraps anything either Windows shell would not pass through literally.
//
// An allowlist rather than a list of metacharacters: cmd.exe and PowerShell disagree about which
// characters are special, and the set that is safe in both is small and easy to state. Double
// quotes work in both for everything here, and no argument in this command contains one, so simple
// wrapping is enough -- if that ever changes this needs the escaping rules too, not just the quotes.
func quoteForShellPaste(arg string) string {
	safe := func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return true
		}
		return strings.ContainsRune(`\/.:_-`, r)
	}
	for _, r := range arg {
		if !safe(r) {
			return `"` + arg + `"`
		}
	}
	return arg
}

// Rights that answer the question doctor is actually asking: can a hook running as the logged-on
// user create the runtime log if it is missing, and append to it if it is not.
//
// Both bits are required. FILE_APPEND_DATA alone cannot create the file, and FILE_WRITE_DATA alone
// on a directory does not permit adding entries to it, so a grant carrying only one of them leaves
// a hook that still fails -- just at a different call.
const writeAccessMask = uint32(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA)

// inheritOnlyACE marks an entry that applies to children but not to the object carrying it.
// Not exported by x/sys/windows, and needed here because such an entry must not read as a grant:
// the directory itself has to be writable for a hook to create the log in the first place.
const inheritOnlyACE = 0x08

type aceVerdict int

const (
	aceIrrelevant aceVerdict = iota
	aceAllowsWrite
	aceDeniesWrite
)

// classifyACE decides what one access control entry says about interactive write access.
//
// Split out from the DACL walk so it can be tested against entry shapes that are tedious to
// construct on a real directory -- inherit-only grants, deny entries, partial masks.
func classifyACE(aceType, aceFlags uint8, mask uint32, matchesSID bool) aceVerdict {
	if !matchesSID || aceFlags&inheritOnlyACE != 0 {
		return aceIrrelevant
	}
	switch aceType {
	case windows.ACCESS_DENIED_ACE_TYPE:
		// Any overlap denies. A deny of one of the two bits is enough to break a hook, and Windows
		// evaluates a matching deny ahead of any grant, so partial credit would be wrong here.
		if mask&writeAccessMask != 0 {
			return aceDeniesWrite
		}
	case windows.ACCESS_ALLOWED_ACE_TYPE:
		// GENERIC_WRITE is normally mapped to its specific rights when an entry is stored, but it
		// survives in entries written by some tooling, and it subsumes both bits.
		if mask&writeAccessMask == writeAccessMask || mask&windows.GENERIC_WRITE != 0 {
			return aceAllowsWrite
		}
	}
	return aceIrrelevant
}

// interactiveUsersCanWrite reports whether the grant is in place, for doctor.
//
// Checked by reading the ACL rather than by attempting a write: doctor runs elevated, so a test
// write would succeed regardless and report healthy for exactly the configuration that is broken.
// That is the trap this check exists to avoid, so it must not fall into it.
//
// Read through the security APIs rather than by parsing `icacls` output. The text form is a
// rendering for humans and varies with the things a rendering varies with: it resolves the SID to a
// localized account name, so a correctly granted directory reads as ungranted on a German or
// Japanese install, and it prints an expanded rights list instead of the short `(M)` alias whenever
// the mask is not exactly one of the aliases. Both produce the same wrong answer -- doctor telling
// an administrator their working endpoint is broken -- and neither is visible from an English test
// machine, which is every machine this would be tested on.
func interactiveUsersCanWrite(dir string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access control list on %s: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("read the discretionary access control list on %s: %w", dir, err)
	}
	if dacl == nil {
		// A NULL DACL grants everyone full control. Alarming, and worth surfacing elsewhere, but
		// the question here is only whether hooks can write -- and they can.
		return true, nil
	}

	interactive, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		return false, fmt.Errorf("resolve the INTERACTIVE security identifier: %w", err)
	}

	allowed := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, fmt.Errorf("read access control entry %d on %s: %w", i, dir, err)
		}
		// Every ACE type this cares about carries its SID at the same offset; the field is named
		// SidStart because the identifier is variable length and runs past the struct.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch classifyACE(ace.Header.AceType, ace.Header.AceFlags, uint32(ace.Mask), sid.Equals(interactive)) {
		case aceDeniesWrite:
			// A deny anywhere in the list settles it, whatever else grants.
			return false, nil
		case aceAllowsWrite:
			allowed = true
		}
	}
	return allowed, nil
}
