package cmd

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A system install configures root's agent runtime, so identifying the human who ran it is what
// makes the collector capture anything. These assert the identification cannot quietly succeed
// with a user that does not exist, or quietly claim root.
func TestResolveConsoleUserRejectsUnusableAccounts(t *testing.T) {
	for _, name := range []string{"", "  ", "root", "beacon-no-such-user-9d2f"} {
		info, ok, err := resolveConsoleUser(name)
		if err != nil {
			t.Fatalf("resolveConsoleUser(%q) error = %v, want a clean not-found", name, err)
		}
		if ok {
			t.Errorf("resolveConsoleUser(%q) reported %+v; root and unknown users are not console users", name, info)
		}
	}
}

func TestResolveConsoleUserAcceptsTheCurrentUser(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.Username == "root" || u.HomeDir == "" {
		t.Skip("needs a non-root current user with a home directory")
	}
	info, ok, err := resolveConsoleUser(u.Username)
	if err != nil || !ok {
		t.Fatalf("resolveConsoleUser(%q) = %+v, %v, %v; want the current user resolved", u.Username, info, ok, err)
	}
	if info.HomeDir != u.HomeDir {
		t.Errorf("HomeDir = %q, want %q -- the settings file is written there", info.HomeDir, u.HomeDir)
	}
}

// SUDO_USER is what every realistic Linux install path sets (`sudo apt install ./beacon.deb`), so
// it must be preferred over logind. Stubbed at the lookup seam rather than relying on the test
// machine's accounts: as root in a container there is no non-root user to name, and the version of
// this test that used one skipped everywhere it mattered.
func TestLinuxActiveConsoleUserPrefersSudoUser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	var asked []string
	restore := lookupConsoleUser
	lookupConsoleUser = func(name string) (consoleUserInfo, bool, error) {
		asked = append(asked, name)
		return consoleUserInfo{Username: name, HomeDir: "/home/" + name}, true, nil
	}
	t.Cleanup(func() { lookupConsoleUser = restore })

	t.Setenv("SUDO_USER", "operator")
	info, ok, err := linuxActiveConsoleUser()
	if err != nil || !ok {
		t.Fatalf("linuxActiveConsoleUser() = %+v, %v, %v; want SUDO_USER honored", info, ok, err)
	}
	if info.Username != "operator" || info.HomeDir != "/home/operator" {
		t.Errorf("resolved %+v, want the SUDO_USER account", info)
	}
	// Exactly one lookup proves logind was never consulted. Two would mean the ordering is
	// accidental and a machine with an active session could win over the person who ran sudo.
	if len(asked) != 1 || asked[0] != "operator" {
		t.Errorf("lookups = %v, want exactly [operator]", asked)
	}
}

// A SUDO_USER that does not resolve must not fall through to logind. It names who ran the install,
// so silently configuring some other logged-in user instead would point telemetry at the wrong
// person's agent and report success.
func TestLinuxActiveConsoleUserDoesNotFallThroughFromABadSudoUser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	calls := 0
	restore := lookupConsoleUser
	lookupConsoleUser = func(string) (consoleUserInfo, bool, error) {
		calls++
		return consoleUserInfo{}, false, nil
	}
	t.Cleanup(func() { lookupConsoleUser = restore })

	t.Setenv("SUDO_USER", "deleted-account")
	if _, ok, err := linuxActiveConsoleUser(); ok || err != nil {
		t.Fatalf("want an unresolved result, got ok=%v err=%v", ok, err)
	}
	if calls != 1 {
		t.Errorf("lookups = %d, want 1: logind must not be consulted after SUDO_USER is set", calls)
	}
}

func TestDefaultActiveConsoleUserIsSilentOnUnsupportedPlatforms(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Skip("both have real implementations")
	}
	if _, ok, err := defaultActiveConsoleUser(); ok || err != nil {
		t.Fatalf("want a clean not-supported result, got ok=%v err=%v", ok, err)
	}
}

// The Linux fallback must not fail an install when logind is absent -- a container or a minimal
// image has no sessions, and that is a skip, not an error.
func TestLinuxActiveConsoleUserWithoutSudoOrLogindIsNotAnError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	// An empty PATH makes loginctl unfindable, which is the same observable situation as a host
	// without logind.
	t.Setenv("PATH", t.TempDir())
	if _, ok, err := linuxActiveConsoleUser(); err != nil {
		t.Fatalf("err = %v, want nil: an install must not fail because logind is missing", err)
	} else if ok {
		t.Error("reported a console user with no sudo environment and no logind")
	}
	_ = os.Getenv("PATH")
}

// fakeLoginctl puts a stub `loginctl` on PATH that answers list-sessions with the given table and
// reports every session active. A fake binary is the only way to drive this: the resolution shells
// out, and the answer depends on which session logind names first.
func fakeLoginctl(t *testing.T, sessionTable string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"list-sessions) printf '%s' " + shellSingleQuote(sessionTable) + " ;;\n" +
		"show-session) echo active ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "loginctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// A host with both a graphical login and an earlier ssh or service session must resolve the person
// at the machine. loginctl lists sessions oldest-first, so taking the first active one picks the ssh
// session -- and the postinstall would configure the wrong user's agent runtime. The comment claimed
// this ordering before the code implemented it.
func TestLinuxActiveConsoleUserPrefersASeatedSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	// Unseated session listed first, seated second -- the order that exposes the bug.
	fakeLoginctl(t, "  7 1002 remote      \n  3 1001 desktop seat0 tty2\n")

	var asked []string
	restore := lookupConsoleUser
	lookupConsoleUser = func(name string) (consoleUserInfo, bool, error) {
		asked = append(asked, name)
		return consoleUserInfo{Username: name, HomeDir: "/home/" + name}, true, nil
	}
	t.Cleanup(func() { lookupConsoleUser = restore })

	info, ok, err := linuxActiveConsoleUser()
	if err != nil || !ok {
		t.Fatalf("linuxActiveConsoleUser() = %+v, %v, %v; want the seated user", info, ok, err)
	}
	if info.Username != "desktop" {
		t.Errorf("resolved %q, want the seated session's user; lookups=%v", info.Username, asked)
	}
}

// An unseated session is still a real human whose agent runs on that host, so it is accepted when
// there is no seated one -- the preference is an ordering, not a filter.
func TestLinuxActiveConsoleUserAcceptsAnUnseatedSessionWhenItIsAll(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	fakeLoginctl(t, "  7 1002 remote      \n")

	restore := lookupConsoleUser
	lookupConsoleUser = func(name string) (consoleUserInfo, bool, error) {
		return consoleUserInfo{Username: name, HomeDir: "/home/" + name}, true, nil
	}
	t.Cleanup(func() { lookupConsoleUser = restore })

	info, ok, err := linuxActiveConsoleUser()
	if err != nil || !ok || info.Username != "remote" {
		t.Fatalf("linuxActiveConsoleUser() = %+v, %v, %v; want the unseated user accepted",
			info, ok, err)
	}
}
