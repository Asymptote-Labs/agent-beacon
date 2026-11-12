package asymptoteobserve

import (
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

// The writer and the drainer must derive the same directory from the same inputs, and an unsafe
// id must yield none on BOTH sides rather than one side's sanitized guess.
func TestDSHSpoolDirAgreesOnSafeAndUnsafeIDs(t *testing.T) {
	workspace := t.TempDir()
	dir, ok := DSHSpoolDir(workspace, "sess-1")
	if !ok {
		t.Fatal("safe id produced no directory")
	}
	if want := filepath.Join(workspace, ".beacon", "dsh-spool", "sess-1"); dir != want {
		t.Errorf("DSHSpoolDir = %q, want %q", dir, want)
	}
	if _, ok := DSHSpoolDir(workspace, "../evil"); ok {
		t.Error("unsafe id produced a directory")
	}
	if _, ok := DSHSpoolDir("  ", "sess-1"); ok {
		t.Error("empty workspace produced a directory")
	}
}
