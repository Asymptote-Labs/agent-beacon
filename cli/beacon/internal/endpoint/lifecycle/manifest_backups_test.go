package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordManifestBackupsAppendsHookBackupsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := writeManifest(true, Manifest{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks.json")
	backup := hooks + ".beacon.20260917T120000Z.bak"
	for _, p := range []string{hooks, backup} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := RecordManifestBackups(true, []string{hooks}); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) != 1 || m.Backups[0] != backup {
		t.Fatalf("manifest backups = %v, want exactly the hook backup once", m.Backups)
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
	if err := RecordManifestBackups(true, []string{"/nonexistent/hooks.json"}); err != nil {
		t.Fatalf("no manifest means no install to attach to: %v", err)
	}
}
