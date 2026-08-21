package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPathHonorsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ProfilePath)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingProfileReportsNotPrompted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p := Load()
	if p.Prompted() {
		t.Fatalf("Prompted() = true for a missing profile, want false")
	}
	if p.InstallID != "" {
		t.Fatalf("InstallID = %q for a missing profile, want empty", p.InstallID)
	}
}

// A truncated or hand-edited profile must not fail an install. Onboarding is a sales
// signal; refusing to install over it would trade a real user for a lead.
func TestLoadCorruptProfileReportsNotPrompted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ProfilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "onboardi`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if Load().Prompted() {
		t.Fatalf("Prompted() = true for a corrupt profile, want false")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := Profile{
		Onboarding: Onboarding{
			CompletedAt:   "2026-08-07T18:03:11Z",
			Outcome:       OutcomeSubmitted,
			Email:         "shukan@asymptotelabs.ai",
			Usage:         UsageWork,
			BeaconVersion: "v0.0.31",
		},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load()
	if !got.Prompted() {
		t.Fatalf("Prompted() = false after Save, want true")
	}
	if got.Onboarding.Email != in.Onboarding.Email {
		t.Fatalf("Email = %q, want %q", got.Onboarding.Email, in.Onboarding.Email)
	}
	if got.Onboarding.Outcome != OutcomeSubmitted {
		t.Fatalf("Outcome = %q, want %q", got.Onboarding.Outcome, OutcomeSubmitted)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if len(got.InstallID) != 32 {
		t.Fatalf("InstallID = %q, want 32 hex characters", got.InstallID)
	}
}

// The profile holds the operator's email address, so it must not be world-readable.
func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Save(Profile{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(home, ProfilePath))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("profile permissions = %o, want 600", perm)
	}
}

// A profile written by an older build with looser permissions gets tightened rather
// than silently kept, because os.WriteFile only applies its mode on creation.
func TestSaveTightensExistingPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ProfilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Save(Profile{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("profile permissions = %o, want 600", perm)
	}
}

func TestSavePreservesExistingInstallID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Save(Profile{InstallID: "deadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first := Load().InstallID

	if err := Save(Profile{InstallID: first, Onboarding: Onboarding{CompletedAt: "later"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load().InstallID; got != first {
		t.Fatalf("InstallID = %q after resave, want stable %q", got, first)
	}
}

func TestSaveWritesValidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Save(Profile{Onboarding: Onboarding{Outcome: OutcomeSkipped}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ProfilePath))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}
	if _, ok := raw["pending_submission"]; ok {
		t.Fatalf("pending_submission should be omitted when nil, got %v", raw["pending_submission"])
	}
}

func TestNewInstallIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id, err := NewInstallID()
		if err != nil {
			t.Fatalf("NewInstallID: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("NewInstallID() = %q, want 32 hex characters", id)
		}
		if seen[id] {
			t.Fatalf("NewInstallID() returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestEnsureInstallIDKeepsExisting(t *testing.T) {
	p := Profile{InstallID: "cafebabecafebabecafebabecafebabe"}
	got, err := EnsureInstallID(&p)
	if err != nil {
		t.Fatalf("EnsureInstallID: %v", err)
	}
	if got != "cafebabecafebabecafebabecafebabe" {
		t.Fatalf("EnsureInstallID() = %q, want the existing ID", got)
	}
}

func TestEnsureInstallIDMintsWhenMissing(t *testing.T) {
	p := Profile{}
	got, err := EnsureInstallID(&p)
	if err != nil {
		t.Fatalf("EnsureInstallID: %v", err)
	}
	if got == "" || p.InstallID != got {
		t.Fatalf("EnsureInstallID() = %q, profile = %q; want a minted ID stored on the profile", got, p.InstallID)
	}
}
