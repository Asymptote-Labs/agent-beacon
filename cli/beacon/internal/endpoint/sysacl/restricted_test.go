package sysacl

import (
	"strings"
	"testing"
)

const readers = "S-1-5-21-1111111111-2222222222-3333333333-1500"

func TestTheRestrictedPolicyIsSafeByItsOwnRules(t *testing.T) {
	dir := RestrictedLogDirDACL(readers)
	cases := []struct {
		name string
		obj  Object
		lock bool
	}{
		{"directory", Object{Owner: sidAdministrators, Protected: true, DACL: dir}, false},
		{"the runtime log", Object{Owner: sidSystem, DACL: InheritedDACL(dir, false)}, false},
		{"an archive", Object{Owner: sidAdministrators, DACL: InheritedDACL(dir, false)}, false},
		{"the lock", Object{Owner: sidAdministrators, Protected: true, DACL: RestrictedLockDACL()}, true},
		{"directory without a readers group", Object{Owner: sidAdministrators, Protected: true, DACL: RestrictedLogDirDACL("")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problems := RestrictedProblems(tc.obj, readers, tc.lock); len(problems) > 0 {
				t.Fatalf("the restricted ACL fails its own check: %v", problems)
			}
		})
	}
}

// The restricted directory passes nothing to ordinary users: no read, no append. Reading is half of
// what the mode keeps from them.
func TestTheRestrictedDirectoryGrantsOrdinaryUsersNothing(t *testing.T) {
	for _, ace := range RestrictedLogDirDACL(readers) {
		if coversInteractiveUsers(ace.SID) {
			t.Fatalf("the restricted directory grants %s 0x%x", ace.SID, ace.Mask)
		}
	}
	if CanAppend(RestrictedLogDirDACL(readers), false, true) {
		t.Fatal("hooks could still append directly to a restricted log")
	}
	for _, ace := range RestrictedLogDirDACL(readers) {
		if ace.SID == readers && ace.Mask&readerForbidden != 0 {
			t.Fatalf("the readers group may do more than read: 0x%x", ace.Mask&readerForbidden)
		}
	}
	for _, ace := range RestrictedLockDACL() {
		if !trustedPrincipal(ace.SID) {
			t.Fatalf("the lock grants %s, who could hold it and stall the collector", ace.SID)
		}
	}
}

func TestRestrictedProblemsCatchesWhatLeaksTheLog(t *testing.T) {
	file := InheritedDACL(RestrictedLogDirDACL(readers), false)
	cases := []struct {
		name string
		obj  Object
		lock bool
		want string
	}{
		{"the shared grant left on a file",
			Object{Owner: sidAdministrators, DACL: with(file, ACE{Type: aceTypeAllowed, Flags: aceInherited, Mask: logAppend, SID: sidInteractive})},
			false, sidInteractive},
		{"read for Users",
			Object{Owner: sidAdministrators, DACL: with(file, ACE{Type: aceTypeAllowed, Mask: readExecute, SID: sidUsers})},
			false, sidUsers},
		{"read for one account",
			Object{Owner: sidAdministrators, DACL: with(file, ACE{Type: aceTypeAllowed, Mask: fileReadData, SID: someUser})},
			false, someUser},
		{"the readers group may write",
			Object{Owner: sidAdministrators, DACL: []ACE{{Type: aceTypeAllowed, Mask: logAppend, SID: readers}}},
			false, "beyond reading"},
		{"the readers group may read the lock",
			Object{Owner: sidAdministrators, Protected: true, DACL: with(RestrictedLockDACL(), ACE{Type: aceTypeAllowed, Mask: readExecute, SID: readers})},
			true, readers},
		{"a grant the directory only passes on",
			Object{Owner: sidAdministrators, Protected: true, DACL: with(RestrictedLogDirDACL(readers), ACE{Type: aceTypeAllowed, Flags: aceObjectInherit | aceInheritOnly, Mask: fileReadData, SID: sidAuthenticated})},
			false, sidAuthenticated},
		{"an ordinary owner",
			Object{Owner: someUser, DACL: file},
			false, "owned by"},
		{"no DACL",
			Object{Owner: sidAdministrators, NullDACL: true},
			false, "no DACL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := RestrictedProblems(tc.obj, readers, tc.lock)
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("problems = %v, want one naming %q", problems, tc.want)
			}
		})
	}
}

// A deny never makes the log easier to reach, and the readers SID is compared without regard to
// case, the way Windows prints SIDs.
func TestRestrictedProblemsIgnoresDeniesAndCase(t *testing.T) {
	obj := Object{Owner: sidAdministrators, DACL: with(InheritedDACL(RestrictedLogDirDACL(readers), false),
		ACE{Type: aceTypeDenied, Mask: fileAllAccess, SID: sidEveryone},
		ACE{Type: aceTypeAllowed, Mask: readExecute, SID: strings.ToLower(readers)})}
	if problems := RestrictedProblems(obj, readers, false); len(problems) > 0 {
		t.Fatalf("problems = %v", problems)
	}
}

func TestRestrictedSDDLIsTheDocumentedACL(t *testing.T) {
	got := SDDL(sidAdministrators, true, RestrictedLogDirDACL(readers))
	want := "O:S-1-5-32-544D:PAI(A;OICI;0x1f01ff;;;S-1-5-18)(A;OICI;0x1f01ff;;;S-1-5-32-544)(A;OICI;0x1200a9;;;" + readers + ")"
	if got != want {
		t.Fatalf("SDDL = %s\nwant   %s", got, want)
	}
}

// with is dacl plus aces, in a slice of its own.
func with(dacl []ACE, aces ...ACE) []ACE {
	return append(append([]ACE(nil), dacl...), aces...)
}
