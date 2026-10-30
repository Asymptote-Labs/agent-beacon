//go:build windows

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
)

// A rollback that puts back a config holding a HEC token puts it back private. The mode it records
// says nothing on Windows, so restoring by mode alone would hand the token to every user.
func TestRollbackRestoresAPrivateConfigPrivate(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skipf("open the process token: %v", err)
	}
	elevated := token.IsElevated()
	token.Close()
	if !elevated {
		t.Skip("writing a file owned by Administrators needs an elevated shell")
	}
	root := filepath.Join(t.TempDir(), "Beacon")
	if err := sysacl.SecureSystemTree(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := atomicfile.WriteSystemPrivate(path, []byte(`{"token":"hec"}`)); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotFile(path)
	if !snapshot.Private {
		t.Fatal("the snapshot of a private config does not record it as private")
	}
	// What a failed install leaves behind: a new config written the ordinary way.
	if err := atomicfile.Write(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreFile(path, snapshot)
	if problems, err := sysacl.InspectPrivateFile(path); err != nil || len(problems) > 0 {
		t.Fatalf("the restored config: %v, %v", problems, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != `{"token":"hec"}` {
		t.Fatalf("restored content = %q, %v", data, err)
	}
}
