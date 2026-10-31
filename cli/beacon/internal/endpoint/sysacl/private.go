package sysacl

import "fmt"

// Private files and directories in the machine-wide tree.
//
// Most of %ProgramData%\Beacon is readable by every user on purpose: hooks run the hooks binary
// from it, and the CLI reads config.json as whoever runs it. A few things written there must not be
// readable: config.json and the collector config once they carry a Splunk or Falcon HEC token, the
// Beacon Cloud credentials, and the copies a system backfill makes of the console user's SQLite
// session stores. On POSIX those are root-owned 0600 files in 0700 directories. On Windows chmod
// changes nothing but the read-only attribute, so a "0600" file there inherits the tree's Users read
// grant like everything else, and these are the entries it carries instead.

// PrivateDACL is the protected DACL on a private file or directory: SYSTEM and Administrators full
// control, nothing else. On a directory both entries are inherited by everything created in it.
func PrivateDACL(dir bool) []ACE {
	var flags uint8
	if dir {
		flags = aceObjectInherit | aceContainerInherit
	}
	return []ACE{
		{Type: aceTypeAllowed, Flags: flags, Mask: fileAllAccess, SID: sidSystem},
		{Type: aceTypeAllowed, Flags: flags, Mask: fileAllAccess, SID: sidAdministrators},
	}
}

// PrivateProblems lists every reason obj lets an account other than SYSTEM and Administrators reach
// it. Any grant counts, reading included: reading is what a private file is kept from. Empty means
// it is private.
func PrivateProblems(obj Object) []string {
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
		if ace.Mask != 0 {
			out = append(out, fmt.Sprintf("grants %s access (0x%x) to a file only SYSTEM and Administrators may read", ace.SID, ace.Mask))
		}
	}
	return out
}
