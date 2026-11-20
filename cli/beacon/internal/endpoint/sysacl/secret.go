package sysacl

// SecretFileDACL is the protected DACL on a machine-wide file that holds a credential -- a HEC
// token in config.json, the rendered collector config, the OTLP forward's headers: SYSTEM and
// Administrators, nobody else. The tree's DACL lets every user read files below it, and the 0600
// mode Beacon asks for means nothing on Windows.
func SecretFileDACL() []ACE {
	return RestrictedLockDACL()
}

// SecretDirDACL is the protected DACL on a machine-wide directory whose contents only SYSTEM and
// Administrators may read, such as the system backfill's copies of the console user's SQLite
// session stores: SecretFileDACL's two entries, inherited by every file and directory created in
// it.
func SecretDirDACL() []ACE {
	dacl := SecretFileDACL()
	for i := range dacl {
		dacl[i].Flags |= aceObjectInherit | aceContainerInherit
	}
	return dacl
}

// UserSecretFileDACL is the protected DACL on a file in one account's own Beacon state that holds a
// credential, such as a user-mode endpoint's Beacon Cloud device key: that account and SYSTEM,
// nobody else. What a profile passes on can include Administrators and, on a redirected or
// hand-made home, wider groups; the 0600 mode Beacon asks for means nothing on Windows.
func UserSecretFileDACL(userSID string) []ACE {
	return []ACE{
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: userSID},
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: sidSystem},
	}
}

// UserSecretDirDACL is UserSecretFileDACL inherited by every file and directory created below.
func UserSecretDirDACL(userSID string) []ACE {
	dacl := UserSecretFileDACL(userSID)
	for i := range dacl {
		dacl[i].Flags |= aceObjectInherit | aceContainerInherit
	}
	return dacl
}

// daclSDDL renders a DACL with no owner, so a descriptor applied with it leaves the owner alone (an
// ordinary account may not make anyone else the owner) and one used at creation takes the creator.
func daclSDDL(protected bool, dacl []ACE) string {
	return SDDL("", protected, dacl)[len("O:"):]
}
