package fxsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultSessionsDirFollowsFxProfileLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this one on Windows

	dir, err := DefaultSessionsDir()
	if err != nil {
		t.Fatalf("DefaultSessionsDir: %v", err)
	}
	if want := filepath.Join(home, ".fx", "sessions"); dir != want {
		t.Errorf("DefaultSessionsDir = %q, want %q", dir, want)
	}
	// Looking must not create anything. The directory's absence is Beacon's evidence that fx has
	// not run for this user, and a probe that creates it destroys that evidence -- and leaves a
	// stray directory in the home of someone who does not use fx.
	if _, err := os.Stat(filepath.Join(home, ".fx")); !os.IsNotExist(err) {
		t.Errorf("looking up the sessions directory created %s", filepath.Join(home, ".fx"))
	}
}

func TestListReturnsOnlySessionDirectoriesThatHoldALog(t *testing.T) {
	root := t.TempDir()
	lines := []string{sessionStartedLine(1, "/repo")}
	writeSession(t, root, "1770000000000-1-aaaa", lines, "")
	// A directory fx created before writing the log: real, and nothing to read yet.
	if err := os.MkdirAll(filepath.Join(root, "1770000000001-2-bbbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stray file at the top level, and a name that is not a valid fx session id.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, root, "has spaces", lines, "")

	store := &Store{Dir: root}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("List returned %d sessions, want 1: %+v", len(refs), refs)
	}
	if refs[0].ID != "1770000000000-1-aaaa" {
		t.Errorf("session id = %q", refs[0].ID)
	}
	if refs[0].SizeBytes != logBytes(lines) {
		t.Errorf("size = %d, want %d", refs[0].SizeBytes, logBytes(lines))
	}
}

// A missing sessions directory means fx has not run here. Callers report that; it is not an error
// and must not read like one.
func TestListOnAMissingDirectoryReturnsNothingAndNoError(t *testing.T) {
	store := &Store{Dir: filepath.Join(t.TempDir(), "absent")}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("List returned %d sessions, want 0", len(refs))
	}
	if store.Exists() {
		t.Error("Exists reported a directory that is not there")
	}
}

func TestListOrdersSessionsByLastChange(t *testing.T) {
	root := t.TempDir()
	lines := []string{sessionStartedLine(1, "/repo")}
	for _, id := range []string{"1770000000002-c-cccc", "1770000000000-a-aaaa", "1770000000001-b-bbbb"} {
		writeSession(t, root, id, lines, "")
	}
	// Modification times, not names, are the ordering key: a session resumed today sorts after one
	// created today and abandoned, whatever their ids say.
	stamp := func(id string, unixMS int64) {
		path := filepath.Join(root, id, EventsFileName)
		when := os.Chtimes(path, timeFromMillis(unixMS), timeFromMillis(unixMS))
		if when != nil {
			t.Fatalf("chtimes: %v", when)
		}
	}
	stamp("1770000000002-c-cccc", 1770000009000)
	stamp("1770000000000-a-aaaa", 1770000007000)
	stamp("1770000000001-b-bbbb", 1770000008000)

	store := &Store{Dir: root}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := []string{refs[0].ID, refs[1].ID, refs[2].ID}
	want := []string{"1770000000000-a-aaaa", "1770000000001-b-bbbb", "1770000000002-c-cccc"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List order = %v, want %v", got, want)
		}
	}
}

func TestValidateSessionIDMatchesFxOwnRule(t *testing.T) {
	for _, id := range []string{"a", "1770000000000-1770000000000000000-a1b2c3d4e5f60718", "session.v3", ".hidden", "a_b-c.d"} {
		if err := ValidateSessionID(id); err != nil {
			t.Errorf("ValidateSessionID(%q) = %v, want it accepted", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", "../outside", "/tmp/outside", "nested/session", `nested\session`, "a b", "a\x00b", strings.Repeat("a", 256)} {
		if err := ValidateSessionID(id); err == nil {
			t.Errorf("ValidateSessionID(%q) accepted a name that is not an fx session id", id)
		}
	}
}

// The manifest's byte count is the committed watermark: fx appends a frame and then advances it, so
// bytes past it are a write in flight. Reading to the watermark rather than to EOF is what keeps a
// sweep from decoding a frame fx has not finished writing.
func TestReadStopsAtTheCommittedWatermark(t *testing.T) {
	root := t.TempDir()
	committed := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}
	inFlight := usageCheckpointLine(3, 10, 5, 0.03)
	all := append(append([]string{}, committed...), inFlight)
	manifest := manifestJSON(testGeneration, logBytes(committed), 2, "/repo")
	writeSession(t, root, testSessionID, all, manifest)

	store := &Store{Dir: root}
	refs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].Manifest == nil {
		t.Fatalf("expected one session with a manifest, got %+v", refs)
	}
	events, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want the 2 committed ones", len(events))
	}
	if events[1].Kind != KindHistoryTurnCommitted {
		t.Errorf("second event kind = %q", events[1].Kind)
	}
	if stats.Malformed != 0 {
		t.Errorf("stats.Malformed = %d, want 0", stats.Malformed)
	}
}

// A manifest can describe more of the log than exists: a restored backup, a copied session
// directory, an interrupted sync. Trusting the count outright would read past EOF and report a
// readable session as unreadable, so the watermark is clamped to the file.
func TestReadClampsAWatermarkThatOverrunsTheFile(t *testing.T) {
	root := t.TempDir()
	lines := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2)}
	manifest := manifestJSON(testGeneration, logBytes(lines)*4, 9, "/repo")
	writeSession(t, root, testSessionID, lines, manifest)

	store := &Store{Dir: root}
	refs, _ := store.List()
	events, stats, err := store.Read(refs[0])
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	if stats.Malformed != 0 {
		t.Errorf("stats.Malformed = %d, want 0", stats.Malformed)
	}
}

// No manifest at all is normal for a session fx has just created, and the log is still readable.
// The whole file is read in that case, minus any unterminated tail.
func TestReadWithoutAManifestFallsBackToTheWholeLog(t *testing.T) {
	root := t.TempDir()
	lines := []string{sessionStartedLine(1, "/repo"), assistantTurnLine(2), usageCheckpointLine(3, 1, 1, 0.0)}
	writeSession(t, root, testSessionID, lines, "")

	store := &Store{Dir: root}
	refs, _ := store.List()
	if refs[0].Manifest != nil {
		t.Fatal("expected no manifest")
	}
	events, _, err := store.Read(refs[0])
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("read %d events, want 3", len(events))
	}
}

// A manifest whose schema version moved is not partially trusted. Its byte count is what bounds the
// log read, and reading the log whole is a correct fallback while trusting a number from a format
// that changed is not.
func TestManifestFromAnotherSchemaVersionIsRejectedRatherThanGuessedAt(t *testing.T) {
	root := t.TempDir()
	lines := []string{sessionStartedLine(1, "/repo")}
	manifest := strings.Replace(manifestJSON(testGeneration, logBytes(lines), 1, "/repo"), `"schema_version":3`, `"schema_version":4`, 1)
	writeSession(t, root, testSessionID, lines, manifest)

	if _, err := ReadManifest(filepath.Join(root, testSessionID, ManifestFileName)); err == nil {
		t.Fatal("a manifest from an unknown schema version was accepted")
	}

	store := &Store{Dir: root}
	refs, _ := store.List()
	if refs[0].Manifest != nil {
		t.Error("List attached an unreadable manifest")
	}
	events, _, err := store.Read(refs[0])
	if err != nil || len(events) != 1 {
		t.Fatalf("Read fell over on a session with an unreadable manifest: %d events, err %v", len(events), err)
	}
}

func TestReadManifestRefusesAnOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	if err := os.WriteFile(path, make([]byte, MaxManifestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("an oversized manifest was read")
	}
}

func TestReadManifestRejectsANegativeWatermark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ManifestFileName)
	manifest := strings.Replace(manifestJSON(testGeneration, 0, 1, "/repo"), `"event_log_bytes":0`, `"event_log_bytes":-1`, 1)
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("a negative event_log_bytes was accepted")
	}
}

func TestNewStoreDefaultsToFxProfileWhenNoDirectoryIsGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	store, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if want := filepath.Join(home, ".fx", "sessions"); store.Dir != want {
		t.Errorf("store.Dir = %q, want %q", store.Dir, want)
	}

	explicit, err := NewStore("/somewhere/else")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if explicit.Dir != "/somewhere/else" {
		t.Errorf("store.Dir = %q, want the caller's directory", explicit.Dir)
	}
}

// timeFromMillis is a test helper rather than an import so the ordering test can stamp file times
// without pulling a time dependency into the package's own code.
func timeFromMillis(ms int64) time.Time { return time.UnixMilli(ms) }
