//go:build windows

package cmd

// chownUserConfigArtifacts is a no-op on Windows, and the reason it can be is specific rather than
// general.
//
// The POSIX version exists because a system-mode install runs as root and leaves root-owned files
// in a user's home directory, which that user then cannot edit. Windows has no uid/gid to assign --
// ownership is a SID and access is an ACL -- but it also does not create the problem in the same
// way: a file written under %USERPROFILE% inherits that profile's ACL, which already grants the
// profile's owner full control. So there is nothing to hand back.
//
// This is not the same question as whether a *system* log directory is writable by a non-admin
// agent process. That one Windows does get wrong by default, and it is a real gap rather than a
// non-issue -- but it belongs with the system paths, not here.
func chownUserConfigArtifacts(_ consoleUserInfo, _ string) error {
	return nil
}
