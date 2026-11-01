package sysacl

// SecretFileDACL is the protected DACL on a machine-wide file that holds a credential -- a HEC
// token in config.json, the rendered collector config, the OTLP forward's headers: SYSTEM and
// Administrators, nobody else. The tree's DACL lets every user read files below it, and the 0600
// mode Beacon asks for means nothing on Windows.
func SecretFileDACL() []ACE {
	return RestrictedLockDACL()
}
