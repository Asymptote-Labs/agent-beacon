//go:build windows

package loggroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeAccounts stands in for the local account database: lookups answer from groups, and net.exe
// adds to it.
func fakeAccounts(t *testing.T, groups map[string]string) *[]string {
	t.Helper()
	var calls []string
	prevLookup, prevRun := lookupLocal, run
	t.Cleanup(func() { lookupLocal, run = prevLookup, prevRun })
	lookupLocal = func(name string) (string, bool, error) {
		sid, ok := groups[name]
		return sid, ok, nil
	}
	run = func(path string, args ...string) (string, error) {
		calls = append(calls, filepath.Base(path)+" "+strings.Join(args, " "))
		if !strings.EqualFold(filepath.Base(path), "net.exe") || !filepath.IsAbs(path) {
			t.Fatalf("ran %s; want net.exe from the system directory", path)
		}
		groups[args[1]] = "S-1-5-21-1-2-3-1500"
		return "", nil
	}
	return &calls
}

func TestEnsureCreatesTheLocalGroupOnce(t *testing.T) {
	groups := map[string]string{}
	calls := fakeAccounts(t, groups)
	_, created, err := Ensure(Name)
	if err != nil || !created {
		t.Fatalf("Ensure = created %v, %v", created, err)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "net.exe localgroup "+Name+" /add") {
		t.Fatalf("calls = %q", *calls)
	}
	sid, found, err := SID(Name)
	if err != nil || !found || sid != "S-1-5-21-1-2-3-1500" {
		t.Fatalf("SID = %q, %v, %v", sid, found, err)
	}
	if _, created, err := Ensure(Name); err != nil || created {
		t.Fatalf("second Ensure = created %v, %v; want the existing group kept", created, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("net.exe ran again: %q", *calls)
	}
}

// The real account database: a group that does not exist is not found, and Ensure creates a real
// local group that lookups then find, once. Needs an elevated shell, which the Windows CI runner is.
func TestEnsureAgainstThisMachine(t *testing.T) {
	if _, found, err := lookupLocal("beacon-test-no-such-group"); err != nil || found {
		t.Fatalf("missing group: found %v, %v", found, err)
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skip(err)
	}
	elevated := token.IsElevated()
	token.Close()
	if !elevated {
		t.Skip("creating a local group needs an elevated shell")
	}
	name := fmt.Sprintf("beacon-test-%d", os.Getpid())
	t.Cleanup(func() {
		system, _ := windows.GetSystemDirectory()
		_, _ = run(filepath.Join(system, "net.exe"), "localgroup", name, "/delete")
	})
	if _, created, err := Ensure(name); err != nil || !created {
		t.Fatalf("Ensure = created %v, %v", created, err)
	}
	sid, found, err := SID(name)
	if err != nil || !found || !strings.HasPrefix(sid, "S-1-5-21-") {
		t.Fatalf("SID(%s) = %q, %v, %v; want a local account-domain SID", name, sid, found, err)
	}
	if _, created, err := Ensure(name); err != nil || created {
		t.Fatalf("second Ensure = created %v, %v", created, err)
	}
}

func TestAddMemberAddsToTheLocalGroup(t *testing.T) {
	calls := fakeAccounts(t, map[string]string{Name: "S-1-5-21-1-2-3-1500"})
	if err := AddMember(Name, `CORP\alice`); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != "net.exe localgroup "+Name+` CORP\alice /add` {
		t.Fatalf("calls = %q", *calls)
	}
}

// The console account an interactive MSI install adds is named from what the person typed at setup,
// so it may hold a space or a non-ASCII letter. It reaches net.exe as one argument.
func TestAddMemberAcceptsTheNamesWindowsGivesLocalAccounts(t *testing.T) {
	fakeAccounts(t, map[string]string{Name: "S-1-5-21-1-2-3-1500"})
	var got [][]string
	run = func(_ string, args ...string) (string, error) {
		got = append(got, append([]string(nil), args...))
		return "", nil
	}
	for _, account := range []string{`DESKTOP-AB12\John Smith`, `PC\José`} {
		if err := AddMember(Name, account); err != nil {
			t.Fatalf("AddMember(%q) = %v", account, err)
		}
	}
	if len(got) != 2 || got[0][2] != `DESKTOP-AB12\John Smith` || got[1][2] != `PC\José` || len(got[0]) != 4 {
		t.Fatalf("net.exe arguments = %q", got)
	}
}

func TestAddMemberTreatsAnExistingMemberAsSuccess(t *testing.T) {
	fakeAccounts(t, map[string]string{Name: "S-1-5-21-1-2-3-1500"})
	run = func(path string, args ...string) (string, error) {
		return "", &toolError{command: path + " " + strings.Join(args, " "), err: errors.New("exit status 2"),
			Stderr: "System error 1378 has occurred.\r\n\r\nThe specified account name is already a member of the group."}
	}
	if err := AddMember(Name, "alice"); err != nil {
		t.Fatalf("AddMember = %v, want success for an existing member", err)
	}
	// In another display language the sentence is translated but the number is not.
	run = func(path string, args ...string) (string, error) {
		return "", &toolError{command: path + " " + strings.Join(args, " "), err: errors.New("exit status 2"),
			Stderr: "Systemfehler 1378 aufgetreten.\r\n\r\nDer angegebene Kontoname ist bereits ein Mitglied der Gruppe."}
	}
	if err := AddMember(Name, "alice"); err != nil {
		t.Fatalf("AddMember (German net.exe) = %v, want success for an existing member", err)
	}
}

// Only net.exe's own error number counts as "already a member": never a 1378 in the account name,
// the command line, or another error's text. Otherwise an account that does not exist would be
// reported as added, and the operator told a log shipper can read the log when it cannot.
func TestAddMemberReportsOtherFailuresEvenWith1378InTheName(t *testing.T) {
	fakeAccounts(t, map[string]string{Name: "S-1-5-21-1-2-3-1500"})
	cases := []struct{ account, stderr string }{
		// The name is on the command line, which Error() includes.
		{"svc1378", "System error 1332 has occurred.\r\n\r\nNo mapping between account names and security IDs was done."},
		{`CORP\svc1378`, "System error 1332 has occurred."},
		// net.exe echoing the name, with or without its domain.
		{`CORP\svc1378`, "There is no such global user or group: svc1378.\r\n\r\nMore help is available by typing NET HELPMSG 3783."},
		{"app1378", "The account app1378 could not be added."},
		{"alice", "System error 5 has occurred.\r\n\r\nAccess is denied."},
		{"alice", ""},
	}
	for _, c := range cases {
		stderr := c.stderr
		run = func(path string, args ...string) (string, error) {
			return "", &toolError{command: path + " " + strings.Join(args, " "), err: errors.New("exit status 2"), Stderr: stderr}
		}
		if err := AddMember(Name, c.account); err == nil {
			t.Errorf("AddMember(%q) with net.exe saying %q = nil, want the failure reported", c.account, c.stderr)
		}
	}
	// A plain error from run (one that is not a toolError) is never read as a net.exe error number.
	run = func(string, ...string) (string, error) {
		return "", fmt.Errorf("net.exe localgroup %s svc1378 /add: 1378", Name)
	}
	if err := AddMember(Name, "svc1378"); err == nil {
		t.Error("AddMember with an untyped error mentioning 1378 = nil, want the failure reported")
	}
}
