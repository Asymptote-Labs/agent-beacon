//go:build windows

package sysacl

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A private file reads back from Windows as Administrators-owned, protected, and granting SYSTEM and
// Administrators alone.
func TestCreatePrivateFileReadsBackPrivate(t *testing.T) {
	requireElevated(t)
	root := filepath.Join(t.TempDir(), "Beacon")
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "otelcol.yaml")
	f, err := CreatePrivateFile(path)
	if err != nil {
		t.Fatalf("CreatePrivateFile: %v", err)
	}
	if _, err := f.WriteString("token: hec\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	obj := mustRead(t, path)
	if obj.Owner != sidAdministrators || !obj.Protected || !reflect.DeepEqual(obj.DACL, PrivateDACL(false)) {
		t.Fatalf("private file reads back owner %s protected %v DACL %+v, want Administrators, protected, %+v",
			obj.Owner, obj.Protected, obj.DACL, PrivateDACL(false))
	}
	if !IsPrivateFile(path) {
		t.Fatal("IsPrivateFile is false for a file CreatePrivateFile made")
	}
	if _, err := CreatePrivateFile(path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("a second CreatePrivateFile at the same path returned %v, want an exists error", err)
	}

	// A file written there the ordinary way inherits the tree's Users read grant, which is the leak.
	plain := filepath.Join(root, "config.json")
	if err := os.WriteFile(plain, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsPrivateFile(plain) {
		t.Fatal("a 0600 file in the tree reads as private on Windows, which is the bug this guards against")
	}
	problems, err := InspectPrivateFile(plain)
	if err != nil || len(problems) == 0 {
		t.Fatalf("InspectPrivateFile on a Users-readable file = %v, %v; want the Users grant reported", problems, err)
	}
}

// SecurePrivateDir closes a directory that inherited the tree's grants, resets what is already in
// it, and passes the private entries to what is created later.
func TestSecurePrivateDirResetsAndPassesOnPrivacy(t *testing.T) {
	requireElevated(t)
	root := filepath.Join(t.TempDir(), "Beacon")
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "Endpoint", "state", "backfill-snapshots")
	stale := filepath.Join(dir, "beacon-backfill-sqlite-1", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsPrivateFile(stale) {
		t.Fatal("setup: the stale snapshot is already private")
	}

	if err := SecurePrivateDir(dir); err != nil {
		t.Fatalf("SecurePrivateDir: %v", err)
	}
	obj := mustRead(t, dir)
	if obj.Owner != sidAdministrators || !obj.Protected || !reflect.DeepEqual(obj.DACL, PrivateDACL(true)) {
		t.Fatalf("private directory reads back owner %s protected %v DACL %+v", obj.Owner, obj.Protected, obj.DACL)
	}
	for _, path := range []string{filepath.Dir(stale), stale} {
		if problems := PrivateProblems(mustRead(t, path)); len(problems) > 0 {
			t.Errorf("%s kept a grant after SecurePrivateDir: %v", path, problems)
		}
	}
	// Created by this process, so its owner follows the machine's default-owner policy; only what it
	// inherits is the directory's doing.
	fresh := filepath.Join(dir, "new.db")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inheritsPrivacy := func() bool {
		return len(PrivateProblems(Object{Owner: sidAdministrators, DACL: mustRead(t, fresh).DACL})) == 0
	}
	if !inheritsPrivacy() {
		t.Fatalf("a file created in the private directory does not inherit its privacy: %+v", mustRead(t, fresh).DACL)
	}

	// A second run on a private directory is a no-op, not an error, and SecureSystemTree leaves a
	// private directory as it is: granting less than the tree is safe by its rules.
	if err := SecurePrivateDir(dir); err != nil {
		t.Fatalf("SecurePrivateDir again: %v", err)
	}
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	if !inheritsPrivacy() || len(PrivateProblems(mustRead(t, dir))) > 0 {
		t.Fatal("SecureSystemTree widened a private directory back to the tree's grants")
	}
}
