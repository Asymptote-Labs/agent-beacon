package cmd

import (
	"fmt"
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

// fakeLoginctl puts a stub `loginctl` on PATH that reproduces what systemd 255 actually prints.
//
// The fixture matters as much as the assertion here. The first version emitted a 3-column table with
// a blank seat for unseated rows, so the test passed against code that treated any non-empty seat
// column as seated -- while real loginctl prints `-` there and the code misclassified every ssh
// session. These strings were captured from a live logind in a container, not written from memory:
//
//	11 1002 remoteuser -     ""   active online no -
//	 9 1001 tester     seat0 tty7 online no     -
//
// and per session, `show-session -p Name -p State -p Seat` printing self-labeling Key=Value lines.
func fakeLoginctl(t *testing.T, sessions []fakeSession) {
	t.Helper()
	var table, cases strings.Builder
	for _, sess := range sessions {
		seat := sess.seat
		if seat == "" {
			seat = "-" // what loginctl really prints for an unseated session
		}
		fmt.Fprintf(&table, "%s 1000 %s %s tty1 %s no -\n", sess.id, sess.user, seat, sess.state)
		fmt.Fprintf(&cases, "  %s) printf 'Name=%s\\nSeat=%s\\nState=%s\\n' ;;\n",
			sess.id, sess.user, sess.seat, sess.state)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"list-sessions) printf '%s' " + shellSingleQuote(table.String()) + " ;;\n" +
		"show-session)\n  case \"$2\" in\n" + cases.String() + "  esac ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "loginctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type fakeSession struct {
	id, user, seat, state string
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func stubLookup(t *testing.T) {
	t.Helper()
	restore := lookupConsoleUser
	lookupConsoleUser = func(name string) (consoleUserInfo, bool, error) {
		return consoleUserInfo{Username: name, HomeDir: "/home/" + name}, true, nil
	}
	t.Cleanup(func() { lookupConsoleUser = restore })
}

// The case verified against real logind: the person at the machine is seated but reports state
// "online" (they are not the foreground session on their seat), while an ssh login reports "active".
// Requiring "active" therefore picks the *remote* user and rejects the one at the keyboard -- the
// opposite of the intent -- and reading the table's seat column classifies the ssh row as seated,
// which collapses the preference entirely.
func TestLinuxActiveConsoleUserPrefersTheUserAtTheMachine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	stubLookup(t)
	// Listed remote-first, which is what loginctl does: sessions come out oldest first.
	fakeLoginctl(t, []fakeSession{
		{id: "11", user: "remoteuser", seat: "", state: "active"},
		{id: "9", user: "desktop", seat: "seat0", state: "online"},
	})

	info, ok, err := linuxActiveConsoleUser()
	if err != nil || !ok {
		t.Fatalf("linuxActiveConsoleUser() = %+v, %v, %v; want the seated user", info, ok, err)
	}
	if info.Username != "desktop" {
		t.Errorf("resolved %q, want the seated user even though the remote one is \"active\"",
			info.Username)
	}
}

// A foreground seated session outranks a background one, so a shared workstation resolves whoever is
// actually in front.
func TestLinuxActiveConsoleUserPrefersTheForegroundSeatedSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	stubLookup(t)
	fakeLoginctl(t, []fakeSession{
		{id: "3", user: "background", seat: "seat0", state: "online"},
		{id: "5", user: "foreground", seat: "seat0", state: "active"},
	})

	info, _, _ := linuxActiveConsoleUser()
	if info.Username != "foreground" {
		t.Errorf("resolved %q, want the foreground seated session", info.Username)
	}
}

// An unseated session is still a real human whose agent runs on this host, so the preference is an
// ordering and not a filter.
func TestLinuxActiveConsoleUserAcceptsAnUnseatedSessionWhenItIsAll(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	stubLookup(t)
	fakeLoginctl(t, []fakeSession{{id: "7", user: "remoteuser", seat: "", state: "active"}})

	info, ok, err := linuxActiveConsoleUser()
	if err != nil || !ok || info.Username != "remoteuser" {
		t.Fatalf("linuxActiveConsoleUser() = %+v, %v, %v; want the unseated user accepted",
			info, ok, err)
	}
}

// A session mid-transition is nobody to configure, and skipping it must not be mistaken for having
// found them.
func TestLinuxActiveConsoleUserIgnoresClosingSessions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only resolution")
	}
	t.Setenv("SUDO_USER", "")
	stubLookup(t)
	fakeLoginctl(t, []fakeSession{{id: "4", user: "leaving", seat: "seat0", state: "closing"}})

	if info, ok, _ := linuxActiveConsoleUser(); ok {
		t.Errorf("resolved %+v from a closing session", info)
	}
}
