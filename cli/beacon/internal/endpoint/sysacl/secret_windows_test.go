//go:build windows

package sysacl

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A credential file in the machine-wide tree is SYSTEM and Administrators only, whatever the tree
// passes on, and replacing it keeps that: the new file never carries the tree's Users grant.
func TestWriteSecretFileIsSystemAndAdministratorsOnly(t *testing.T) {
	requireElevated(t)
	root := filepath.Join(t.TempDir(), "Beacon")
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "otlp-forward-headers.json")
	for _, body := range []string{`{"headers":{"authorization":"one"}}`, `{"headers":{"authorization":"two"}}`} {
		if err := WriteSecretFile(path, []byte(body)); err != nil {
			t.Fatalf("WriteSecretFile: %v", err)
		}
		obj := mustRead(t, path)
		if !obj.Protected || !reflect.DeepEqual(obj.DACL, SecretFileDACL()) {
			t.Fatalf("secret file carries %+v (protected %v), want only %+v", obj.DACL, obj.Protected, SecretFileDACL())
		}
		if obj.Owner != sidAdministrators {
			t.Fatalf("secret file owned by %s, want Administrators", obj.Owner)
		}
		if !IsSecretFile(path) {
			t.Fatal("IsSecretFile = false for a file WriteSecretFile wrote")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Fatalf("content = %q, %v", got, err)
		}
	}
	// A later pass over the tree keeps the stricter ACL rather than resetting it to the tree's.
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	if obj := mustRead(t, path); !reflect.DeepEqual(obj.DACL, SecretFileDACL()) {
		t.Fatalf("SecureSystemTree reset the secret file to %+v", obj.DACL)
	}
	// A file the tree's DACL covers is not a secret one, so a rollback restores it as it was.
	plain := filepath.Join(root, "plain.json")
	if err := os.WriteFile(plain, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSecretFile(plain) {
		t.Fatal("IsSecretFile = true for a file with the tree's DACL")
	}
	os.Remove(plain)
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

// SecureSecretDir closes a directory that inherited the tree's grants, resets what is already in
// it, and passes the private entries to what is created there later. A later pass over the tree
// leaves it private.
func TestSecureSecretDirResetsAndPassesOnPrivacy(t *testing.T) {
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
	if len(grantsOthers(mustRead(t, stale).DACL)) == 0 {
		t.Fatal("setup: the stale snapshot does not carry the tree's Users grant")
	}

	if err := SecureSecretDir(dir); err != nil {
		t.Fatalf("SecureSecretDir: %v", err)
	}
	obj := mustRead(t, dir)
	if obj.Owner != sidAdministrators || !obj.Protected || !reflect.DeepEqual(obj.DACL, SecretDirDACL()) {
		t.Fatalf("secret directory reads back owner %s protected %v DACL %+v", obj.Owner, obj.Protected, obj.DACL)
	}
	for _, path := range []string{filepath.Dir(stale), stale} {
		if others := grantsOthers(mustRead(t, path).DACL); len(others) > 0 {
			t.Errorf("%s kept a grant after SecureSecretDir: %+v", path, others)
		}
	}
	fresh := filepath.Join(dir, "beacon-backfill-sqlite-2", "state.db")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if others := grantsOthers(mustRead(t, fresh).DACL); len(others) > 0 {
		t.Fatalf("a file created in the secret directory inherits %+v", others)
	}

	// Running it again changes nothing, and a pass over the tree does not widen it back.
	if err := SecureSecretDir(dir); err != nil {
		t.Fatalf("SecureSecretDir again: %v", err)
	}
	if err := SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	if obj := mustRead(t, dir); !obj.Protected || !reflect.DeepEqual(obj.DACL, SecretDirDACL()) {
		t.Fatalf("SecureSystemTree changed the secret directory to %+v (protected %v)", obj.DACL, obj.Protected)
	}
	if others := grantsOthers(mustRead(t, fresh).DACL); len(others) > 0 {
		t.Fatalf("SecureSystemTree widened a file in the secret directory: %+v", others)
	}
}

// grantsOthers lists the allow entries in dacl for anyone but SYSTEM and Administrators.
func grantsOthers(dacl []ACE) []ACE {
	var out []ACE
	for _, ace := range dacl {
		if ace.Type != aceTypeDenied && ace.SID != sidSystem && ace.SID != sidAdministrators && ace.Mask != 0 {
			out = append(out, ace)
		}
	}
	return out
}
