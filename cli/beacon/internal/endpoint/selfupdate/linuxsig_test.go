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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(filepath.Join("testdata", "signed-release", filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if tampered && strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			data = []byte(strings.Replace(string(data), "ffff", "eeee", 1))
		}
		w.Write(data)
	}))
	defer srv.Close()

	old := releaseKeyFingerprints
	releaseKeyFingerprints = []string{testReleaseKeyFingerprint}
	defer func() { releaseKeyFingerprints = old }()
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
	tampered = true
	if err := verify(); err == nil {
		t.Fatal("tampered checksums.txt accepted")
	}
}
