package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordManifestBackupsAppendsHookBackupsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := writeManifest(true, Manifest{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks.json")
	// An older backup from a previous install holds already-rewritten hooks and must be ignored;
	// the one stamped by this install is the pre-install state to restore.
	older := hooks + ".beacon.20260901T080000Z.bak"
	backup := hooks + ".beacon.20260917T120000Z.bak"
	legacy := hooks + ".beacon.bak"
	if err := os.WriteFile(older, []byte(`{"older":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{hooks, backup, legacy} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := RecordManifestBackups(true, []string{hooks}, since); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) != 1 || m.Backups[0] != backup {
		t.Fatalf("manifest backups = %v, want exactly this install's backup once", m.Backups)
	}
	// Uninstall restores it (KeepConfig false) and removes the manifest.
	if err := os.WriteFile(hooks, []byte(`{"rewritten":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(UninstallOptions{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(hooks)
	if string(restored) != "{}" {
		t.Fatalf("uninstall should restore the hook file from its backup, got %s", restored)
	}
}

func TestRecordManifestBackupsWithoutAManifestIsANoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := RecordManifestBackups(true, []string{"/nonexistent/hooks.json"}, time.Now()); err != nil {
		t.Fatalf("no manifest means no install to attach to: %v", err)
	}
}

func TestBackupTimestampParsesOnlyStampedNames(t *testing.T) {
	if _, ok := backupTimestamp("/x/hooks.json.beacon.bak"); ok {
		t.Fatal("legacy unstamped backup has no timestamp")
	}
	stamp, ok := backupTimestamp("/x/hooks.json.beacon.20260917T161813Z.bak")
	if !ok || stamp != time.Date(2026, 9, 17, 16, 18, 13, 0, time.UTC) {
		t.Fatalf("stamp = %v ok=%t", stamp, ok)
	}
}
