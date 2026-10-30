//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

func requireElevated(t *testing.T) {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skipf("open the process token: %v", err)
	}
	defer token.Close()
	if !token.IsElevated() {
		t.Skip("creating a file owned by Administrators needs an elevated shell")
	}
}

// A file in the machine-wide tree that already carries the tree's Users read grant -- what an
// endpoint installed before this change has -- is replaced by one only SYSTEM and Administrators
// can read, and the same holds for a fresh write.
func TestWriteSystemPrivateReplacesAUsersReadableFile(t *testing.T) {
	requireElevated(t)
	root := filepath.Join(t.TempDir(), "Beacon")
	if err := sysacl.SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "otelcol.yaml")
	if err := os.WriteFile(path, []byte("token: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sysacl.IsPrivateFile(path) {
		t.Fatal("setup: a 0600 file in the tree is already private")
	}
	if err := WriteSystemPrivate(path, []byte("token: new\n")); err != nil {
		t.Fatalf("WriteSystemPrivate: %v", err)
	}
	if problems, err := sysacl.InspectPrivateFile(path); err != nil || len(problems) > 0 {
		t.Fatalf("after WriteSystemPrivate: %v, %v", problems, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "token: new\n" {
		t.Fatalf("content = %q, %v", data, err)
	}

	fresh := filepath.Join(root, "config.json")
	if err := WriteSystemPrivate(fresh, []byte("{}")); err != nil {
		t.Fatalf("WriteSystemPrivate of a new file: %v", err)
	}
	if !sysacl.IsPrivateFile(fresh) {
		t.Fatal("a new file written private is not private")
	}

	// Write, used once the token is gone, puts the tree's grants back: nothing secret is left.
	if err := Write(fresh, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sysacl.IsPrivateFile(fresh) {
		t.Fatal("Write kept the private ACL, so a config without a token would stay unreadable to users")
	}
}
