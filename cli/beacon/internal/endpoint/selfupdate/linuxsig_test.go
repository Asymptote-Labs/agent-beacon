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
