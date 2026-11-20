//go:build windows

package sysacl

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A user-mode credential directory and file are this account and SYSTEM only, set without any
// privilege, and what was already in the directory is reset to inherit exactly that.
func TestUserSecretDirAndFileAreThisAccountAndSystemOnly(t *testing.T) {
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "asymptote")
	if err := os.MkdirAll(filepath.Join(dir, "vector-data"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(dir, "vector-data", "checkpoints.json")
	if err := os.WriteFile(before, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SecureUserSecretDir(dir); err != nil {
		t.Fatalf("SecureUserSecretDir: %v", err)
	}
	obj := mustRead(t, dir)
	if !obj.Protected || !reflect.DeepEqual(obj.DACL, UserSecretDirDACL(sid)) {
		t.Fatalf("directory carries %+v (protected %v), want %+v", obj.DACL, obj.Protected, UserSecretDirDACL(sid))
	}
	child := mustRead(t, before)
	for _, ace := range child.DACL {
		if !strings.EqualFold(ace.SID, sid) && ace.SID != sidSystem {
			t.Fatalf("existing file still grants %s: %+v", ace.SID, child.DACL)
		}
	}
	path := filepath.Join(dir, "vector-secrets.json")
	for _, body := range []string{`{"device_key":"one"}`, `{"device_key":"two"}`} {
		if err := WriteUserSecretFile(path, []byte(body)); err != nil {
			t.Fatalf("WriteUserSecretFile: %v", err)
		}
		obj := mustRead(t, path)
		if !obj.Protected || !reflect.DeepEqual(obj.DACL, UserSecretFileDACL(sid)) {
			t.Fatalf("secret file carries %+v (protected %v), want %+v", obj.DACL, obj.Protected, UserSecretFileDACL(sid))
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Fatalf("content = %q, %v", got, err)
		}
	}
}
