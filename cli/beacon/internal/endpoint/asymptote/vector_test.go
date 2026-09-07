package asymptote

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolateVectorDiscovery hides the machine's own Vector installs from FindVector.
func isolateVectorDiscovery(t *testing.T) {
	t.Helper()
	t.Setenv(VectorBinEnv, "")
	oldSearch, oldLook := vectorSearchPaths, lookPath
	vectorSearchPaths = func() []string { return nil }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { vectorSearchPaths, lookPath = oldSearch, oldLook })
}

func TestFindVectorPrefersExplicitThenEnvAndChecksVersion(t *testing.T) {
	isolateVectorDiscovery(t)
	explicit := fakeVector(t, "0.56.0", 0)
	fromEnv := fakeVector(t, "0.58.2", 0)
	t.Setenv(VectorBinEnv, fromEnv)

	got, err := FindVector(explicit)
	if err != nil || got.Path != explicit || got.Version != "0.56.0" {
		t.Fatalf("explicit: %+v %v", got, err)
	}
	got, err = FindVector("")
	if err != nil || got.Path != fromEnv || got.Version != "0.58.2" {
		t.Fatalf("env: %+v %v", got, err)
	}
	t.Setenv(VectorBinEnv, "")
	if _, err := FindVector(""); !errors.Is(err, ErrVectorNotFound) && !strings.Contains(err.Error(), "vector") {
		t.Fatalf("expected not found without candidates, got %v", err)
	}
}

func TestFindVectorRejectsTooOldOrUnparsableVersions(t *testing.T) {
	old := fakeVector(t, "0.44.0", 0)
	if _, err := FindVector(old); err == nil || !strings.Contains(err.Error(), "0.50.0 or newer") {
		t.Fatalf("expected too-old error, got %v", err)
	}
	dir := t.TempDir()
	garbage := filepath.Join(dir, "vector")
	if err := os.WriteFile(garbage, []byte("#!/bin/sh\necho not a version\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindVector(garbage); err == nil || !strings.Contains(err.Error(), "could not read a version") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.56.0", "0.56.0", 0},
		{"0.57.0", "0.56.0", 1},
		{"0.55.9", "0.56.0", -1},
		{"1.0.0", "0.99.9", 1},
		{"0.56", "0.56.0", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestValidateVectorConfigSurfacesVectorOutput(t *testing.T) {
	ok := fakeVector(t, "0.56.0", 0)
	if err := ValidateVectorConfig(ok, "/tmp/x.toml"); err != nil {
		t.Fatalf("validate ok: %v", err)
	}
	bad := fakeVector(t, "0.56.0", 3)
	if err := ValidateVectorConfig(bad, "/tmp/x.toml"); err == nil || !strings.Contains(err.Error(), "vector validate failed") {
		t.Fatalf("expected validate failure, got %v", err)
	}
}

func TestEnrollmentStoreUsesPrivatePermissionsAndAtomicWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := LoadEnrollment(true); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("expected ErrNotEnrolled, got %v", err)
	}
	if err := WriteSecrets(true, "not-a-device-key"); err == nil {
		t.Fatal("must refuse to store a non-device key")
	}
	if err := WriteSecrets(true, "bcn_device_abcdefgh_"+strings.Repeat("k", 43)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SecretsPath(true))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets mode: %v %v", info, err)
	}
	entries, _ := os.ReadDir(Dir(true))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	key, err := ReadDeviceKey(true)
	if err != nil || !strings.HasPrefix(key, "bcn_device_abcdefgh_") {
		t.Fatalf("ReadDeviceKey = %q %v", key, err)
	}
	if err := RemoveState(true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Dir(true)); !os.IsNotExist(err) {
		t.Fatal("RemoveState should delete the directory")
	}
}

// TestDefaultVectorSearchPathsPrefersTheBeaconVectorKeg pins where a Homebrew-installed
// Vector is looked for. The tap mirrors Vector as the beacon-vector keg rather than as a
// formula named `vector`, because Homebrew allows one keg by that name and a `vector`
// dependency broke `brew upgrade beacon` for anyone already running vectordotdev/brew's.
// That keg keeps its binary in libexec, which Homebrew never links, so nothing finds it
// except this path -- and it is tried ahead of any linked vector because it is the copy
// whose version the tap pins, and FindVector treats a too-old binary as an error rather
// than a reason to keep looking.
func TestDefaultVectorSearchPathsPrefersTheBeaconVectorKeg(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", filepath.Join("/fake", "brew"))
	paths := defaultVectorSearchPaths()

	if len(paths) == 0 || paths[0] != PackagedVectorPath {
		t.Fatalf("defaultVectorSearchPaths() = %q, want the packaged Vector first", paths)
	}
	keg := filepath.Join("/fake", "brew", "opt", TapVectorFormula, "libexec", "vector")
	if !slices.Contains(paths, keg) {
		t.Fatalf("defaultVectorSearchPaths() = %q, want it to include the beacon-vector keg %q", paths, keg)
	}
	if linked := filepath.Join("/fake", "brew", "bin", "vector"); !slices.Contains(paths, linked) {
		t.Fatalf("defaultVectorSearchPaths() = %q, want it to include a linked Vector %q", paths, linked)
	}

	lastKeg, firstLinked := -1, len(paths)
	for i, path := range paths {
		switch {
		case strings.Contains(path, filepath.Join("opt", TapVectorFormula)):
			lastKeg = i
		case path != PackagedVectorPath && strings.HasSuffix(path, filepath.Join("bin", "vector")):
			firstLinked = min(firstLinked, i)
		}
	}
	if lastKeg > firstLinked {
		t.Errorf("defaultVectorSearchPaths() = %q, want every beacon-vector keg before every linked Vector; "+
			"interleaving lets an old linked Vector in one prefix mask the pinned keg in the next", paths)
	}
}

// TestHomebrewPrefixesDeduplicates covers HOMEBREW_PREFIX being set to the platform default,
// which would otherwise search the same prefix twice.
func TestHomebrewPrefixesDeduplicates(t *testing.T) {
	prefixes := homebrewPrefixes()
	if len(prefixes) == 0 {
		t.Skip("no Homebrew prefixes on this platform")
	}
	t.Setenv("HOMEBREW_PREFIX", prefixes[0])
	got := homebrewPrefixes()
	seen := make(map[string]bool, len(got))
	for _, prefix := range got {
		if seen[prefix] {
			t.Fatalf("homebrewPrefixes() = %q, want no duplicates", got)
		}
		seen[prefix] = true
	}
	if len(got) != len(prefixes) {
		t.Errorf("homebrewPrefixes() = %q, want the same %d prefixes as the unset default %q", got, len(prefixes), prefixes)
	}
}
