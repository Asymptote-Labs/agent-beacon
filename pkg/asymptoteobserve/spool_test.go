package asymptoteobserve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidDSHSessionIDForSpool(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"60fdb1c1-0000-5000-8000-000000000000", true},
		{"session-abc_123.v2", true},
		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{"a/b", false},
		{"a\\b", false},
		{"../escape", false},
		{"has space", false},
		{"line\nbreak", false},
		{string(make([]byte, 129)), false},
	}
	for _, tc := range cases {
		if got := ValidDSHSessionIDForSpool(tc.id); got != tc.want {
			t.Errorf("ValidDSHSessionIDForSpool(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// The writer and the drainer must derive the same path from the same inputs, and an unsafe
// id must yield no path on BOTH sides rather than one side's sanitized guess.
func TestDSHSpoolPathAgreesOnSafeAndUnsafeIDs(t *testing.T) {
	workspace := t.TempDir()
	path, ok := DSHSpoolPath(workspace, "sess-1")
	if !ok {
		t.Fatal("safe id produced no path")
	}
	want := filepath.Join(workspace, ".beacon", "dsh-spool", "sess-1.jsonl")
	if path != want {
		t.Errorf("DSHSpoolPath = %q, want %q", path, want)
	}
	if _, ok := DSHSpoolPath(workspace, "../evil"); ok {
		t.Error("unsafe id produced a path")
	}
	if _, ok := DSHSpoolPath("", "sess-1"); ok {
		t.Error("empty workspace produced a path")
	}
}

// Pending bytes count data files oldest-first (rotated archives before the live file) and
// never count lock files, which live in the same directory by design.
func TestDSHSpoolFilesAndPendingBytes(t *testing.T) {
	workspace := t.TempDir()
	dir := DSHSpoolDir(workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DSHSpoolPendingBytes(workspace, "sess-1"); got != 0 {
		t.Errorf("PendingBytes with no files = %d, want 0", got)
	}
	base, _ := DSHSpoolPath(workspace, "sess-1")
	files := map[string]string{
		base + ".1":    "aaaaa",
		base:           "bb",
		base + ".lock": "notdata", // lock file: must never be listed or counted
		"other.jsonl":  "ccc",     // another session's spool: not ours
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := DSHSpoolFiles(workspace, "sess-1")
	want := []string{base + ".1", base} // archive first, live file last
	if len(got) != len(want) {
		t.Fatalf("DSHSpoolFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DSHSpoolFiles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if pending := DSHSpoolPendingBytes(workspace, "sess-1"); pending != 7 {
		t.Errorf("PendingBytes = %d, want 7 (5+2; lock and other-session files excluded)", pending)
	}
}
