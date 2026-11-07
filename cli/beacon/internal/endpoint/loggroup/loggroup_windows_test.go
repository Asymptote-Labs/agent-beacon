//go:build windows

package loggroup

import (
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

func TestAddMemberTreatsAnExistingMemberAsSuccess(t *testing.T) {
	fakeAccounts(t, map[string]string{Name: "S-1-5-21-1-2-3-1500"})
	run = func(string, ...string) (string, error) {
		return "", fmt.Errorf("net.exe: exit status 2: System error 1378 has occurred. The specified account name is already a member of the group.")
	}
	if err := AddMember(Name, "alice"); err != nil {
		t.Fatalf("AddMember = %v, want success for an existing member", err)
	}
}
