package dshsession

import (
	"path/filepath"
	"testing"
)

// TestClassifySessionFile pins the name shape the store matches.
//
// DSH versions the session file: it writes `session.v3.jsonl.zstd`, and the `v3`
// is the session format version. Discovery compared the name for equality against
// the two unversioned spellings, so it found none of the 25 sessions in a real
// store and returned that as success with an empty list. This table is what
// should make that recurrence fail loudly.
func TestClassifySessionFile(t *testing.T) {
	cases := []struct {
		name       string
		ok         bool
		compressed bool
	}{
		{"session.jsonl", true, false},
		{"session.jsonl.zstd", true, true},
		{"session.v3.jsonl", true, false},
		{"session.v3.jsonl.zstd", true, true},
		{"session.v42.jsonl.zstd", true, true},
		{"session.lock", false, false},
		{"session.v3.jsonl.zstd.tmp", false, false},
		{"session.v3.jsonl.gz", false, false},
		{"sessions.jsonl", false, false},
		{"session.v.jsonl", false, false},
		{"session_v3.jsonl", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		compressed, ok := classifySessionFile(tc.name)
		if ok != tc.ok {
			t.Errorf("classifySessionFile(%q) ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if ok && compressed != tc.compressed {
			t.Errorf("classifySessionFile(%q) compressed = %v, want %v", tc.name, compressed, tc.compressed)
		}
	}
}

// TestListFindsSessionsWrittenTheWayDSHWritesThem is the end-to-end half: a
// directory laid out as DSH leaves it -- the versioned compressed record beside
// its lock file -- must be discovered, marked compressed, and read.
func TestListFindsSessionsWrittenTheWayDSHWritesThem(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--home-me-repo--", "session-abc")
	writeBytes(t, filepath.Join(dir, "session.lock"), nil)
	writeBytes(t, filepath.Join(dir, "session.v3.jsonl.zstd"),
		zstdFrame(t, []byte(record("session", map[string]interface{}{"id": "versioned-1", "cwd": "/repo"})+"\n")))

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 session, got %d: %+v", len(refs), refs)
	}
	if !refs[0].Compressed {
		t.Error("a .zstd record must be marked compressed")
	}
	if refs[0].ID != "versioned-1" {
		t.Errorf("want the id from the record, got %q", refs[0].ID)
	}
	records, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
}

// TestListPrefersTheCompressedFrame keeps the precedence the collector already
// documented: when a directory holds both spellings, the compressed record is the
// one that counts, whichever order the walk visits them in.
func TestListPrefersTheCompressedFrame(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "ws", "session-both")
	writeBytes(t, filepath.Join(dir, "session.v3.jsonl"),
		[]byte(record("session", map[string]interface{}{"id": "plain"})+"\n"))
	writeBytes(t, filepath.Join(dir, "session.v3.jsonl.zstd"),
		zstdFrame(t, []byte(record("session", map[string]interface{}{"id": "compressed"})+"\n")))

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("want 1 session for one directory, got %d", len(refs))
	}
	if !refs[0].Compressed {
		t.Error("the compressed frame must win")
	}
	if refs[0].ID != "compressed" {
		t.Errorf("want the compressed record's id, got %q", refs[0].ID)
	}
}
