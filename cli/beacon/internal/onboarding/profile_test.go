package onboarding

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func TestLoadMissingProfileReportsNotPrompted(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

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
	testenv.SetHome(t, home)
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
	testenv.SetHome(t, t.TempDir())

	in := Profile{
		Onboarding: Onboarding{
			CompletedAt:   "2026-08-07T18:03:11Z",
			Outcome:       OutcomeSubmitted,
			Email:         "shukan@asymptotelabs.ai",
			Usage:         UsageWork,
			BeaconVersion: "v0.0.31",
			Destination:   DestinationAsymptote,
			PrivacyMode:   "metadata_only",
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
	if got.Onboarding.Destination != DestinationAsymptote || got.Onboarding.PrivacyMode != "metadata_only" {
		t.Fatalf("managed settings = %#v", got.Onboarding)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if len(got.InstallID) != 32 {
		t.Fatalf("InstallID = %q, want 32 hex characters", got.InstallID)
	}
}

// A profile written by an older build with looser permissions gets tightened rather
// than silently kept, because os.WriteFile only applies its mode on creation.
func TestSaveTightensExistingPermissions(t *testing.T) {
	testenv.RequirePOSIXFileModes(t)
	home := t.TempDir()
	testenv.SetHome(t, home)
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
	testenv.SetHome(t, t.TempDir())

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

// install_id is the signup endpoint's dedupe key and the handle quoted to request
// deletion, so two machines must never mint the same one: a fixed or zeroed ID would
// merge every install into one server row.
func TestSaveMintsADistinctInstallIDPerMachine(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		home := t.TempDir()
		testenv.SetHome(t, home)
		if err := Save(Profile{}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(home, ProfilePath))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var onDisk struct {
			InstallID string `json:"install_id"`
		}
		if err := json.Unmarshal(data, &onDisk); err != nil {
			t.Fatalf("profile is not valid JSON: %v\n%s", err, data)
		}
		if raw, err := hex.DecodeString(onDisk.InstallID); err != nil || len(raw) != 16 {
			t.Fatalf("install_id = %q, want 32 hex characters", onDisk.InstallID)
		}
		if seen[onDisk.InstallID] {
			t.Fatalf("two machines minted the same install_id %q", onDisk.InstallID)
		}
		seen[onDisk.InstallID] = true
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
