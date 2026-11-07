package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testdata/signed-release is a release signed by a throwaway test key with this fingerprint.
const testReleaseKeyFingerprint = "492264C703AE4B306CF804C9E0F25BE9BE03411C"

func TestVerifyReleaseSignature(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release signatures are verified on Linux only")
	}
	if _, err := exec.LookPath("gpgv"); err != nil {
		t.Skip("gpgv is not installed")
	}
	tampered := false
	// staleChecksums makes the next N checksums.txt downloads come from a different upload than
	// the signature, as a client sees while finalize replaces the pair.
	staleChecksums := 0
	checksumFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join("testdata", "signed-release", filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			checksumFetches++
			stale := staleChecksums > 0
			if stale {
				staleChecksums--
			}
			if tampered || stale {
				data = []byte(strings.Replace(string(data), "ffff", "eeee", 1))
			}
		}
		w.Write(data)
	}))
	defer srv.Close()

	old := releaseKeyFingerprints
	releaseKeyFingerprints = []string{testReleaseKeyFingerprint}
	defer func() { releaseKeyFingerprints = old }()
	oldDelay := releaseSignatureRetryDelay
	releaseSignatureRetryDelay = 0
	defer func() { releaseSignatureRetryDelay = oldDelay }()
	verify := func() error {
		a := NewApplier("1.0.0")
		a.StageDir = t.TempDir()
		pkg := filepath.Join(t.TempDir(), "download.deb")
		if err := os.WriteFile(pkg, []byte("package\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return a.verifyReleaseSignature(context.Background(), srv.URL+"/v1.0.0/beacon_1.0.0_linux_amd64.deb", pkg)
	}

	if err := verify(); err != nil {
		t.Fatalf("validly signed package refused: %v", err)
	}

	// One mismatched fetch, as during finalize's swap: the second fetch gets a matching pair.
	staleChecksums, checksumFetches = 1, 0
	if err := verify(); err != nil {
		t.Fatalf("package refused after a mismatched pair was fetched once: %v", err)
	}
	if checksumFetches != 2 {
		t.Fatalf("checksums.txt fetched %d times, want 2", checksumFetches)
	}
	// Mismatched on both fetches: refused, with no third attempt.
	staleChecksums, checksumFetches = 2, 0
	if err := verify(); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	if checksumFetches != 2 {
		t.Fatalf("checksums.txt fetched %d times, want 2", checksumFetches)
	}

	tampered = true
	if err := verify(); err == nil {
		t.Fatal("tampered checksums.txt accepted")
	}
}

// withoutReleaseKeyPin clears the embedded pin for a test that exercises another part of Apply
// against a test server that serves no signed checksums.txt.
func withoutReleaseKeyPin(t *testing.T) {
	t.Helper()
	old := releaseKeyFingerprints
	releaseKeyFingerprints = nil
	t.Cleanup(func() { releaseKeyFingerprints = old })
}

// The pin is Beacon's release key, and the docs and the apt repository's committed key name the
// same one, so none of them can be changed alone.
func TestReleaseKeyPinMatchesPublishedKey(t *testing.T) {
	const want = "07BEA34ED6A411F4C066D98322F94195DCDC8F45"
	if len(releaseKeyFingerprints) != 1 || releaseKeyFingerprints[0] != want {
		t.Fatalf("release-key.fingerprint pins %v, want [%s]", releaseKeyFingerprints, want)
	}
	repo := filepath.Join("..", "..", "..", "..", "..")
	docs, err := os.ReadFile(filepath.Join(repo, "docs", "platforms", "linux.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docs), want) {
		t.Errorf("docs/platforms/linux.mdx does not publish the pinned fingerprint %s", want)
	}
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg is not installed; skipping the apt keyring check")
	}
	out, err := exec.Command("gpg", "--batch", "--homedir", t.TempDir(), "--with-colons", "--show-keys",
		filepath.Join(repo, "packaging", "linux", "apt", "beacon-archive-keyring.asc")).Output()
	if err != nil {
		t.Fatalf("gpg --show-keys: %v", err)
	}
	var primary []string
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "pub:") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "fpr:") {
			primary = append(primary, strings.Split(lines[i+1], ":")[9])
		}
	}
	if len(primary) != 1 || primary[0] != want {
		t.Errorf("beacon-archive-keyring.asc holds %v, want [%s]", primary, want)
	}
}

// With the real pin, a Linux update refuses a release that has no signed checksums.txt and never
// runs the package installer.
func TestApplyRefusesUnsignedReleaseOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release signatures are verified on Linux only")
	}
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := nativeManifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := NewApplier("0.0.1")
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "system.jsonl")
	var installed []string
	a.run = func(_ context.Context, name string, _ ...string) (string, error) {
		installed = append(installed, filepath.Base(name))
		return "", nil
	}
	_, err := a.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify release signature") {
		t.Fatalf("Apply error = %v, want a release signature failure", err)
	}
	for _, name := range installed {
		if name == "dpkg" || name == "rpm" {
			t.Fatalf("ran %s before the signature check passed", name)
		}
	}
}
