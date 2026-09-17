package lifecycle

import (
	"errors"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/writer"
	"os"
	"path/filepath"
	"runtime"
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

func TestRestoreBackupsUsesTheOldestBackupPerTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hooks.json")
	oldest := target + ".beacon.20260901T080000Z.bak"
	newer := target + ".beacon.20260917T120000Z.bak"
	for p, body := range map[string]string{target: "rewritten", oldest: "original", newer: "already-beacon"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restoreBackups([]string{newer, oldest})
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatalf("restore must use the oldest backup, got %q", got)
	}
	legacy := target + ".beacon.bak"
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreBackups([]string{oldest, legacy})
	got, _ = os.ReadFile(target)
	if string(got) != "legacy" {
		t.Fatalf("an unstamped legacy backup counts as oldest, got %q", got)
	}
}

func TestAppendManifestBackupsCarriesExistingOnesForward(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := writeManifest(true, Manifest{UserMode: true}); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(t.TempDir(), "hooks.json.beacon.20260901T080000Z.bak")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "hooks.json.beacon.20260901T090000Z.bak")
	if err := AppendManifestBackups(true, []string{keep, gone, keep}); err != nil {
		t.Fatal(err)
	}
	m, _ := ReadManifest(true)
	if len(m.Backups) != 1 || m.Backups[0] != keep {
		t.Fatalf("manifest should carry the existing backup once and drop the missing one: %v", m.Backups)
	}
}

// A repair (uninstall + install with the same content) must not forget the backup that holds
// the pre-Beacon hooks; that is the very path the Homebrew upgrade caveat sends people down.
func TestRepairKeepsThePreviousManifestBackups(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeInventoryJob(t, true)
	opts := InstallOptions{UserMode: true, LogPath: filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"), Harnesses: []string{}, GRPCPort: freePort(t), HTTPPort: freePort(t), CollectorPath: collectorPath, StartService: false}
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(home, ".codex", "hooks.json.beacon.20260917T100000Z.bak")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendManifestBackups(true, []string{backup}); err != nil {
		t.Fatal(err)
	}
	if _, err := Repair(opts); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range m.Backups {
		if b == backup {
			found = true
		}
	}
	if !found {
		t.Fatalf("repair dropped the hook backup from the manifest: %v", m.Backups)
	}
	// And a plain reinstall over the existing manifest keeps it too.
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	m, _ = ReadManifest(true)
	found = false
	for _, b := range m.Backups {
		if b == backup {
			found = true
		}
	}
	if !found {
		t.Fatalf("reinstall dropped the hook backup from the manifest: %v", m.Backups)
	}
}

// A failed reinstall rolls back to the state from minutes ago, not to a pre-Beacon backup from
// an earlier install. The historical backups are persisted for Uninstall, never handed to Rollback.
func TestFailedReinstallDoesNotRevertToHistoricalBackups(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("install preflight is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	collectorPath := filepath.Join(home, "bin", "beacon-otelcol")
	if err := os.MkdirAll(filepath.Dir(collectorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collectorPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeInventoryJob(t, true)
	opts := InstallOptions{UserMode: true, LogPath: filepath.Join(home, ".beacon", "endpoint", "logs", "runtime.jsonl"), Harnesses: []string{}, GRPCPort: freePort(t), HTTPPort: freePort(t), CollectorPath: collectorPath, StartService: false}
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	// A hook file with an old, pre-Beacon backup registered by the previous install.
	hooks := filepath.Join(home, ".codex", "hooks.json")
	ancient := hooks + ".beacon.20260901T080000Z.bak"
	if err := os.MkdirAll(filepath.Dir(hooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ancient, []byte("ancient"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendManifestBackups(true, []string{ancient}); err != nil {
		t.Fatal(err)
	}
	// Make the reinstall fail at its last step so Rollback runs.
	oldAppend := appendInstallEvent
	appendInstallEvent = func(schema.Event, writer.Options) (string, error) { return "", errors.New("disk full") }
	t.Cleanup(func() { appendInstallEvent = oldAppend })
	if _, err := Install(opts); err == nil {
		t.Fatal("reinstall should fail")
	}
	got, _ := os.ReadFile(hooks)
	if string(got) != "current" {
		t.Fatalf("rollback reverted a file this run never touched to a historical backup: %q", got)
	}
	// The persisted manifest from the successful install still carries the historical backup.
	m, err := ReadManifest(true)
	if err == nil {
		for _, b := range m.Backups {
			if b == ancient {
				return
			}
		}
	}
}
