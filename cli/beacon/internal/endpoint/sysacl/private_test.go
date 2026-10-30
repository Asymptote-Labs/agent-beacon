package sysacl

import (
	"reflect"
	"testing"
)

func TestThePrivatePolicyIsPrivateByItsOwnRules(t *testing.T) {
	cases := []struct {
		name string
		obj  Object
	}{
		{"a private file", Object{Owner: sidAdministrators, Protected: true, DACL: PrivateDACL(false)}},
		{"a private directory", Object{Owner: sidAdministrators, Protected: true, DACL: PrivateDACL(true)}},
		{"a file created in a private directory", Object{Owner: sidSystem, DACL: InheritedDACL(PrivateDACL(true), false)}},
		{"a directory created in a private directory", Object{Owner: sidAdministrators, DACL: InheritedDACL(PrivateDACL(true), true)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problems := PrivateProblems(tc.obj); len(problems) > 0 {
				t.Fatalf("the private ACL fails its own check: %v", problems)
			}
		})
	}
}

// What a 0600 file in the machine-wide tree carried before: the tree's inherited Users read grant.
// That is the leak, so it must read as one.
func TestAFileInheritingTheTreeIsNotPrivate(t *testing.T) {
	obj := Object{Owner: sidAdministrators, DACL: InheritedDACL(TreeDACL(), false)}
	if problems := PrivateProblems(obj); len(problems) == 0 {
		t.Fatal("a file every user may read passes as private")
	}
	for name, obj := range map[string]Object{
		"owned by a user": {Owner: "S-1-5-21-1-2-3-1001", Protected: true, DACL: PrivateDACL(false)},
		"no DACL":         {Owner: sidAdministrators, NullDACL: true},
		"read for INTERACTIVE": {Owner: sidAdministrators, Protected: true, DACL: append(PrivateDACL(false),
			ACE{Type: aceTypeAllowed, Mask: fileGenericRead, SID: sidInteractive})},
	} {
		if problems := PrivateProblems(obj); len(problems) == 0 {
			t.Errorf("%s passes as private", name)
		}
	}
	denied := Object{Owner: sidAdministrators, Protected: true, DACL: append(PrivateDACL(false),
		ACE{Type: aceTypeDenied, Mask: fileAllAccess, SID: sidUsers})}
	if problems := PrivateProblems(denied); len(problems) > 0 {
		t.Fatalf("a deny entry made a private file less private: %v", problems)
	}
}

func TestThePrivatePolicyGrantsOrdinaryUsersNothing(t *testing.T) {
	for _, dir := range []bool{false, true} {
		for _, ace := range PrivateDACL(dir) {
			if !trustedPrincipal(ace.SID) {
				t.Fatalf("the private DACL grants %s 0x%x", ace.SID, ace.Mask)
			}
		}
	}
	if !reflect.DeepEqual(RestrictedLockDACL(), PrivateDACL(false)) {
		t.Fatal("the restricted log's lock is no longer a private file")
	}
}
