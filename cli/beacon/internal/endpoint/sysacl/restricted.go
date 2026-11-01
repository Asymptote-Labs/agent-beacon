package sysacl

import (
	"fmt"
	"strings"
)

// The restricted log access mode (`--log-access restricted`) on Windows.
//
// In the shared mode the log directory lets INTERACTIVE read and write the data of every file in
// it, so any logged-on user can read every other user's prompts and command output, and overwrite
// bytes already in the log. In the restricted mode the collector, running as SYSTEM, is the only
// writer: hooks hand it their events over the local ingest pipe (package localingest), and the
// directory grants ordinary users nothing at all. SYSTEM and Administrators keep full control, and
// a local readers group (the beacon-log group, which install creates) may read, for log shippers
// that do not run as SYSTEM and for people who should see every user's sessions.
//
// The lock is closer still: SYSTEM and Administrators only. Taking LockFileEx needs only a read
// handle, so a reader that could open the lock could hold it and stall every write the collector
// makes.

// RestrictedLogDirDACL is the protected DACL on the system log directory in the restricted mode.
// Every entry applies to the directory, its files and its subdirectories. readersSID may be empty,
// which leaves the log to SYSTEM and Administrators alone.
func RestrictedLogDirDACL(readersSID string) []ACE {
	dacl := []ACE{
		{Type: aceTypeAllowed, Flags: aceObjectInherit | aceContainerInherit, Mask: fileAllAccess, SID: sidSystem},
		{Type: aceTypeAllowed, Flags: aceObjectInherit | aceContainerInherit, Mask: fileAllAccess, SID: sidAdministrators},
	}
	if readersSID != "" {
		dacl = append(dacl, ACE{Type: aceTypeAllowed, Flags: aceObjectInherit | aceContainerInherit, Mask: readExecute, SID: readersSID})
	}
	return dacl
}

// RestrictedLockDACL is the protected DACL on a restricted log's lock: SYSTEM and Administrators.
func RestrictedLockDACL() []ACE {
	return []ACE{
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: sidSystem},
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: sidAdministrators},
	}
}

// readerForbidden is everything a reader of the restricted log must not hold: any right to write,
// append, remove, rename or re-secure.
const readerForbidden = fileWriteData | fileAppendData | fileWriteEA | fileWriteAttributes | fileDeleteChild |
	accessDelete | writeDAC | writeOwner | accessSystemSecurity | maximumAllowed | genericAll | genericWrite

// RestrictedProblems lists every reason obj -- the restricted log directory, a file of its log
// family, or the lock -- lets an account other than SYSTEM and Administrators reach the log, or
// lets the readers group do more than read (and, on the lock, anything at all). Empty means it is
// as the restricted mode sets it.
//
// Unlike Problems, any grant counts, reading included: keeping other accounts from reading the log
// is half of what the mode is for. An entry counts wherever it applies, so a grant a directory
// only passes on to new files is reported on the directory.
func RestrictedProblems(obj Object, readersSID string, lock bool) []string {
	var out []string
	if !trustedPrincipal(obj.Owner) {
		out = append(out, "owned by "+obj.Owner+", not SYSTEM or Administrators")
	}
	if obj.NullDACL {
		return append(out, "has no DACL, which grants everyone full control")
	}
	for _, ace := range obj.DACL {
		if ace.Type == aceTypeDenied || trustedPrincipal(ace.SID) {
			continue
		}
		if ace.SID == sidCreatorOwner || ace.SID == sidCreatorGroup {
			// Resolved to whoever creates the child, and only SYSTEM and Administrators can.
			continue
		}
		if readersSID != "" && strings.EqualFold(ace.SID, readersSID) && !lock {
			if bad := ace.Mask & readerForbidden; bad != 0 {
				out = append(out, fmt.Sprintf("grants the log readers group %s rights 0x%x beyond reading", ace.SID, bad))
			}
			continue
		}
		if ace.Mask != 0 {
			out = append(out, fmt.Sprintf("grants %s access (0x%x) to a log only SYSTEM, Administrators and the readers group may reach", ace.SID, ace.Mask))
		}
	}
	return out
}
