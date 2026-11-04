//go:build windows

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sysacl"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"golang.org/x/sys/windows"
)

// A system backfill's copy of the console user's SQLite store is readable by SYSTEM and
// Administrators alone. Without that, the copy would inherit whatever its directory passes on: in
// %ProgramData%\Beacon, read for every user.
func TestSystemEndpointBackfillSnapshotsAreSystemAndAdministratorsOnly(t *testing.T) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Skipf("open the process token: %v", err)
	}
	elevated := token.IsElevated()
	token.Close()
	if !elevated {
		t.Skip("setting the snapshot directory's owner needs an elevated shell")
	}

	home := t.TempDir()
	store := filepath.Join(home, ".hermes", "state.db")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}

	var snapshot string
	var others []string
	stubBackfill(t, 1<<20, 1<<20, endpointBackfillRunner{Name: "Hermes Agent", Sync: "beacon endpoint hermes sync", Run: func(endpointBackfillRun) (int, error) {
		var err error
		if snapshot, err = sessionfs.SQLitePath(store); err != nil {
			return 0, err
		}
		// Read while the copy exists: the backfill removes it once the runtime is done.
		for _, path := range []string{snapshot, filepath.Dir(snapshot)} {
			obj, err := sysacl.ReadObject(path)
			if err != nil {
				return 0, err
			}
			for _, ace := range obj.DACL {
				// Type 0 is an allow entry. S-1-5-18 is SYSTEM, S-1-5-32-544 Administrators.
				if ace.Type == 0 && ace.Mask != 0 && ace.SID != "S-1-5-18" && ace.SID != "S-1-5-32-544" {
					others = append(others, path+": "+ace.SID)
				}
			}
		}
		return 0, nil
	}})
	secureBackfillScratchDir = sysacl.SecureSecretDir
	stubConsoleUser(t, consoleUserInfo{Username: "alice", HomeDir: home}, true, nil)

	result := runEndpointBackfill(filepath.Join(t.TempDir(), "runtime.jsonl"), false)
	if snapshot == "" {
		t.Fatalf("no snapshot was made: %+v", result)
	}
	if !strings.HasPrefix(snapshot, endpointBackfillScratchDir()+string(filepath.Separator)) {
		t.Fatalf("snapshot %s is not under the scratch directory %s", snapshot, endpointBackfillScratchDir())
	}
	if len(others) > 0 {
		t.Fatalf("the snapshot is readable by accounts other than SYSTEM and Administrators: %v", others)
	}
}
