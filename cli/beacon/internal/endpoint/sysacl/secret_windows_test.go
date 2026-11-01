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
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}
