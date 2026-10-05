package asymptoteobserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// A spool directory reached through a symlink is refused on both the staging and the
// draining side; a missing one is reported as not existing, which callers treat as "nothing
// staged".
func TestCheckDSHSpoolDirRefusesSymlinks(t *testing.T) {
	workspace := t.TempDir()
	if err := CheckDSHSpoolDir(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing spool: err = %v, want ErrNotExist", err)
	}
	if err := os.MkdirAll(DSHSpoolDir(workspace), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckDSHSpoolDir(workspace); err != nil {
		t.Fatalf("real spool directory refused: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	linked := t.TempDir()
	if err := os.Mkdir(filepath.Join(linked, ".beacon"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, ".beacon", "dsh-spool")); err != nil {
		t.Fatal(err)
	}
	if err := CheckDSHSpoolDir(linked); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked dsh-spool: err = %v, want a refusal", err)
	}
	base, _ := DSHSpoolPath(workspace, "sess-1")
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), base); err != nil {
		t.Fatal(err)
	}
	if files := DSHSpoolFiles(workspace, "sess-1"); len(files) != 0 {
		t.Fatalf("symlinked spool file listed: %v", files)
	}
}

// VerifiedEventID accepts exactly the ids a writer stamps over the line's own bytes.
func TestVerifiedEventID(t *testing.T) {
	event := map[string]interface{}{
		"event":   map[string]interface{}{"action": "prompt.submitted", "kind": "event"},
		"message": "a <b> & c",
		"n":       json.Number("9007199254740993"),
		"session": map[string]interface{}{"id": "sess-1"},
	}
	unstamped, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	event["event"].(map[string]interface{})["id"] = EventIDForLine(unstamped)
	stamped, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := VerifiedEventID(stamped); !ok || id == "" {
		t.Fatalf("writer-stamped id did not verify: %q", id)
	}
	tampered := bytes.Replace(stamped, []byte("sess-1"), []byte("sess-2"), 1)
	if bytes.Equal(tampered, stamped) {
		t.Fatal("tamper did not apply")
	}
	if _, ok := VerifiedEventID(tampered); ok {
		t.Fatal("id verified for a line whose content changed after stamping")
	}
	if _, ok := VerifiedEventID(unstamped); ok {
		t.Fatal("a line with no id verified")
	}
	if _, ok := VerifiedEventID([]byte("not json")); ok {
		t.Fatal("garbage verified")
	}
}
