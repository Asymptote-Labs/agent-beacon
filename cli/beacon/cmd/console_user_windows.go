//go:build windows

package cmd

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// windowsActiveConsoleUser resolves the person whose runtime configuration a system install should
// reach.
//
// This is the gap that made the Linux package install useless: a system endpoint configured the
// *installing* account's Claude Code and Codex settings, which for a package install is root rather
// than the person at the keyboard, so the collector was healthy and captured nothing. Windows has no
// SUDO_USER to answer it with, and the shape of the question is different enough to be worth stating.
//
// Elevation on Windows does not change the account. "Run as administrator" gives the same user a
// token with more privileges, so an admin running `beacon endpoint install --system` from an elevated
// prompt *is* the console user -- no lookup needed, and no guessing. That covers every interactive
// install.
//
// The case that needs real work is an install running as LocalSystem: an MSI custom action, a
// scheduled task, a fleet management agent. There the current account is not a person at all, and the
// interactive session has to be found. Splitting on "am I SYSTEM" rather than on "am I elevated" is
// what makes both paths correct.
func windowsActiveConsoleUser() (consoleUserInfo, bool, error) {
	isSystem, err := runningAsLocalSystem()
	if err != nil {
		return consoleUserInfo{}, false, err
	}
	if !isSystem {
		return currentWindowsUser()
	}
	return windowsConsoleSessionUser()
}

// runningAsLocalSystem reports whether this process's own account is LocalSystem.
//
// Compared by SID rather than by name: the account renders as "NT AUTHORITY\SYSTEM" in English and
// differently in every other display language, and a name comparison would quietly take the wrong
// branch on a localized machine.
func runningAsLocalSystem() (bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false, fmt.Errorf("open this process's access token: %w", err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return false, fmt.Errorf("read the account this process runs as: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, fmt.Errorf("resolve the LocalSystem security identifier: %w", err)
	}
	return user.User.Sid.Equals(system), nil
}

// currentWindowsUser describes the account this process is already running as.
func currentWindowsUser() (consoleUserInfo, bool, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return consoleUserInfo{}, false, fmt.Errorf("open this process's access token: %w", err)
	}
	defer token.Close()
	return describeTokenUser(token)
}

// windowsConsoleSessionUser finds whoever is logged on at the physical console.
//
// The analogue of logind on Linux, and used for the same reason: the account running the install is
// not a person, so the person has to be identified some other way. Reported as absent rather than
// guessed at whenever that fails, which keeps the Linux rule -- an unresolvable user is reported, not
// invented, because configuring the wrong profile writes hooks nobody runs and reports success.
func windowsConsoleSessionUser() (consoleUserInfo, bool, error) {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	// 0xFFFFFFFF means no session is attached to the console, which happens on a headless server and
	// while the machine is at the lock screen mid-boot.
	if sessionID == 0xFFFFFFFF {
		return consoleUserInfo{}, false, nil
	}

	var token windows.Token
	// Needs SYSTEM rights, which is the only branch that reaches here. A failure normally means the
	// session exists but nobody is signed in to it -- session 0 is the services session and has no
	// interactive user -- so it is an absence, not an error to fail an install over.
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return consoleUserInfo{}, false, nil
	}
	defer token.Close()

	return describeTokenUser(token)
}

// describeTokenUser reads the account name and profile directory out of an access token.
//
// The profile directory comes from the token rather than from %SystemDrive%\Users\<name>. That
// convention is a default, not a rule: profiles can be redirected, roam, live on another volume, or
// carry a suffix when a name collides with an existing profile directory. Assembling the path by hand
// would configure a directory the person does not use, and the failure would be silence.
func describeTokenUser(token windows.Token) (consoleUserInfo, bool, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return consoleUserInfo{}, false, fmt.Errorf("read the account from the access token: %w", err)
	}
	account, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return consoleUserInfo{}, false, fmt.Errorf("resolve a name for the account: %w", err)
	}
	home, err := token.GetUserProfileDirectory()
	if err != nil {
		return consoleUserInfo{}, false, fmt.Errorf("read the profile directory for %s: %w", account, err)
	}
	if account == "" || home == "" {
		// Nothing to configure, and saying so beats returning a half-identified user that a caller
		// would then use to build paths.
		return consoleUserInfo{}, false, nil
	}
	return consoleUserInfo{Username: qualifiedAccountName(domain, account), HomeDir: home}, true, nil
}

// qualifiedAccountName renders DOMAIN\user, which is how Windows names an account and how an operator
// reading `endpoint status` will recognize it. The domain is omitted when there is none to report.
func qualifiedAccountName(domain, account string) string {
	if domain == "" {
		return account
	}
	return domain + `\` + account
}
