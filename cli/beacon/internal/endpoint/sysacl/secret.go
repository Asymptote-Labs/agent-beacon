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
