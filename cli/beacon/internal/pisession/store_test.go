package pisession

import (
	"os"
	"path/filepath"
	"testing"
)

// List resolves the fields `beacon endpoint pi status` reports, and Workspace is
// the one it cannot recompute: the status command has no access to the header map.
func TestListResolvesSessionIdentityAndWorkspace(t *testing.T) {
	sessions := t.TempDir()
	writePiJSONL(t, filepath.Join(sessions, "session-1.jsonl"), []string{
		`{"type":"session","id":"session-1","cwd":"/work/project"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"hello"}]}}`,
	})
	// No session header: the id falls back to the filename and the workspace is unknown
	// rather than wrong.
	writePiJSONL(t, filepath.Join(sessions, "session-2.jsonl"), []string{
		`{"type":"message","message":{"role":"user","content":[{"text":"hi"}]}}`,
	})

	store, err := NewStore(sessions)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("len(refs) = %d, want 2", len(refs))
	}

	byID := map[string]SessionRef{}
	for _, ref := range refs {
		byID[ref.ID] = ref
	}
	first, ok := byID["session-1"]
	if !ok {
		t.Fatalf("no ref with id session-1: %#v", refs)
	}
	if first.Workspace != "/work/project" {
		t.Fatalf("Workspace = %q, want /work/project", first.Workspace)
	}
	if first.SizeBytes == 0 || first.ModTimeMS == 0 {
		t.Fatalf("stat fields not populated: %#v", first)
	}
	second, ok := byID["session-2"]
	if !ok {
		t.Fatalf("no ref with id session-2: %#v", refs)
	}
	if second.Workspace != "" {
		t.Fatalf("Workspace = %q, want empty for a transcript with no session header", second.Workspace)
	}
}

func TestListReturnsNothingForAMissingSessionsDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	store, err := NewStore(missing)
	if err != nil {
		t.Fatal(err)
	}
	if store.Exists() {
		t.Fatalf("Exists() = true for %s", missing)
	}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List() on a missing dir returned an error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("len(refs) = %d, want 0", len(refs))
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("List() created %s", missing)
	}
}
