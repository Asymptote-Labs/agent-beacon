package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rewrite of an existing hooks file keeps the previous content beside it, in the same
// <path>.beacon.<timestamp>.bak shape harness configs use, so an uninstall can restore it. A
// rewrite that changes nothing makes no backup and no write; a file that did not exist has
// nothing to keep.
func TestInstallSettingsEndpointHooksBacksUpTheFileItRewrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	backups := func() []string {
		m, _ := filepath.Glob(path + ".beacon.*.bak")
		return m
	}

	if err := installCodexHooks(path, "/opt/beacon/hooks/beacon-hooks", "/var/log/beacon-agent/runtime.jsonl", "/etc/beacon/config.json"); err != nil {
		t.Fatal(err)
	}
	if len(backups()) != 0 {
		t.Fatalf("no file existed before, so nothing to back up: %v", backups())
	}
	system, _ := os.ReadFile(path)

	// A user-mode install repoints the same hooks at the user log: that is the rewrite to keep.
	if err := installCodexHooks(path, "/Users/me/.beacon/endpoint/hooks/beacon-hooks", "/Users/me/.beacon/endpoint/logs/runtime.jsonl", "/Users/me/.beacon/endpoint/config.json"); err != nil {
		t.Fatal(err)
	}
	got := backups()
	if len(got) != 1 {
		t.Fatalf("a changed rewrite must leave one backup, got %v", got)
	}
	kept, _ := os.ReadFile(got[0])
	if string(kept) != string(system) {
		t.Fatalf("backup must hold the pre-rewrite content:\n%s", kept)
	}
	if !strings.Contains(string(kept), "/var/log/beacon-agent/runtime.jsonl") {
		t.Fatal("backup should be the system-install hooks")
	}

	// Same content again: no new backup, file untouched.
	before, _ := os.Stat(path)
	if err := installCodexHooks(path, "/Users/me/.beacon/endpoint/hooks/beacon-hooks", "/Users/me/.beacon/endpoint/logs/runtime.jsonl", "/Users/me/.beacon/endpoint/config.json"); err != nil {
		t.Fatal(err)
	}
	if len(backups()) != 1 {
		t.Fatalf("an unchanged rewrite must not add a backup: %v", backups())
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged rewrite must not touch the file")
	}
}
