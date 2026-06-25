package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// makeBeaconTarball builds a gzipped tar containing opt/beacon/bin/beacon as an
// executable script that prints the given version, returning the bytes + sha256.
func makeBeaconTarball(t *testing.T, version string) ([]byte, string) {
	t.Helper()
	script := "#!/bin/sh\necho \"beacon version " + version + " (test) built on test\"\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "opt/beacon/bin/beacon", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// manifestServer serves a manifest pointing at /artifact.tar.gz and the artifact.
func manifestServer(t *testing.T, version, sha string, artifact []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"schema":1,"version":%q,"team_id":"TEAMID","artifacts":{%q:{"url":%q,"sha256":%q}}}`,
			version, updatecheck.RuntimeArchKey(), srv.URL+"/artifact.tar.gz", sha)
	})
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(artifact)
	})
	return srv
}

func testApplier(t *testing.T, current string, srv *httptest.Server) *Applier {
	t.Helper()
	prefix := t.TempDir()
	a := NewApplier(current)
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = prefix
	a.AllowInsecureTest = true
	a.SkipGatekeeper = true
	a.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	return a
}

func TestApplyHappyPath(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := testApplier(t, "0.0.1", srv)
	res, err := a.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied || res.ToVersion != "9.9.9" {
		t.Fatalf("unexpected result: %+v", res)
	}
	bin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("beacon not installed: %v", err)
	}
	// Telemetry written.
	data, err := os.ReadFile(a.LogPath)
	if err != nil || !strings.Contains(string(data), "update.applied") {
		t.Fatalf("expected update.applied telemetry, got err=%v data=%s", err, data)
	}
}

func TestApplySHA256Mismatch(t *testing.T) {
	artifact, _ := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", "deadbeefbad", artifact)
	defer srv.Close()

	a := testApplier(t, "0.0.1", srv)
	res, err := a.Apply(context.Background())
	if err == nil {
		t.Fatalf("expected checksum error")
	}
	if res.Applied {
		t.Fatalf("must not apply on sha mismatch")
	}
	// System untouched: nothing extracted into the prefix.
	if _, err := os.Stat(filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")); !os.IsNotExist(err) {
		t.Fatalf("install prefix should be untouched, stat err=%v", err)
	}
	if !strings.Contains(string(mustRead(t, a.LogPath)), "update.failed") {
		t.Fatalf("expected update.failed telemetry")
	}
}

func TestApplyUpToDate(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := testApplier(t, "9.9.9", srv) // current == manifest
	res, err := a.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Applied {
		t.Fatalf("should not apply when up to date")
	}
	if !strings.Contains(res.Message, "up to date") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestApplySkipsManifestFetchForDevBuild(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := NewApplier("dev")
	a.ManifestURL = srv.URL + "/manifest.json"

	res, err := a.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if called {
		t.Fatal("manifest server was called for dev build")
	}
	if res.Applied {
		t.Fatalf("dev build should not apply: %+v", res)
	}
}

func TestApplyHealthFailRollsBack(t *testing.T) {
	// New artifact installs a beacon that reports the WRONG version, so the
	// health check (which expects manifest.version 9.9.9) fails and rolls back.
	artifact, sha := makeBeaconTarball(t, "1.1.1")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := testApplier(t, "0.0.1", srv)
	// Pre-seed an OLD binary so a rollback snapshot exists.
	oldBin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if err := os.MkdirAll(filepath.Dir(oldBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldBin, []byte("#!/bin/sh\necho OLD-BINARY\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := a.Apply(context.Background())
	if err == nil {
		t.Fatalf("expected health-check failure")
	}
	if !res.RolledBack {
		t.Fatalf("expected rollback, got %+v", res)
	}
	got := mustRead(t, oldBin)
	if !strings.Contains(string(got), "OLD-BINARY") {
		t.Fatalf("rollback did not restore old binary, got %q", got)
	}
}

func TestApplyGatekeeperAbortsBeforeInstall(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := NewApplier("0.0.1")
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	// SkipGatekeeper stays false; stub the runner so pkgutil fails and records calls.
	var calls []string
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name)
		if name == "pkgutil" {
			return "bad signature", fmt.Errorf("exit 1")
		}
		return "", nil
	}

	if _, err := a.Apply(context.Background()); err == nil {
		t.Fatalf("expected signature verification error")
	}
	for _, c := range calls {
		if c == "installer" {
			t.Fatalf("installer must not run after signature failure; calls=%v", calls)
		}
	}
}

func TestSkipGatekeeperRequiresInsecureFlag(t *testing.T) {
	a := NewApplier("0.0.1")
	a.SkipGatekeeper = true // without AllowInsecureTest
	if _, err := a.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "AllowInsecureTest") {
		t.Fatalf("expected guard error, got %v", err)
	}
}

func TestInsecureApplyRequiresNonRootPrefix(t *testing.T) {
	a := NewApplier("0.0.1")
	a.AllowInsecureTest = true
	if _, err := a.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "non-root install prefix") {
		t.Fatalf("expected non-root prefix guard, got %v", err)
	}
}

func TestRollbackDoesNotRemoveLiveInstallWhenRestorePreparationFails(t *testing.T) {
	a := NewApplier("0.0.1")
	a.AllowInsecureTest = true
	a.InstallPrefix = t.TempDir()
	a.StageDir = t.TempDir()
	liveBin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if err := os.MkdirAll(filepath.Dir(liveBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveBin, []byte("live"), 0o755); err != nil {
		t.Fatal(err)
	}
	var result ApplyResult
	if err := a.rollback(filepath.Join(t.TempDir(), "missing-backup"), &result); err == nil {
		t.Fatal("expected rollback error for missing backup")
	}
	if got := string(mustRead(t, liveBin)); got != "live" {
		t.Fatalf("live install changed after failed rollback prep: %q", got)
	}
	if result.RolledBack {
		t.Fatal("RolledBack should remain false when restore was not completed")
	}
}

func TestRollbackReportsCollectorRestartFailure(t *testing.T) {
	a := NewApplier("0.0.1")
	a.InstallPrefix = t.TempDir()
	a.StageDir = t.TempDir()
	liveBin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if err := os.MkdirAll(filepath.Dir(liveBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveBin, []byte("failed-install"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(a.StageDir, "backup")
	backupBin := filepath.Join(backup, "bin/beacon")
	if err := os.MkdirAll(filepath.Dir(backupBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupBin, []byte("backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		return "launchctl failed", fmt.Errorf("exit 1")
	}
	var result ApplyResult
	if err := a.rollback(backup, &result); err == nil {
		t.Fatal("expected restart failure")
	}
	if got := string(mustRead(t, liveBin)); got != "backup" {
		t.Fatalf("filesystem rollback did not restore backup: %q", got)
	}
	if !result.RolledBack {
		t.Fatal("RolledBack should be true when filesystem rollback completed")
	}
}

func TestVersionLineMatches(t *testing.T) {
	out := "beacon version 0.0.69 (abc1234) built on 2026-01-01"
	if !versionLineMatches(out, "0.0.69") {
		t.Error("exact version should match")
	}
	if !versionLineMatches(out, "v0.0.69") {
		t.Error("v-prefixed want should match")
	}
	// The substring bug: an expected 0.0.6 must NOT match a 0.0.69 binary.
	if versionLineMatches(out, "0.0.6") {
		t.Error("0.0.6 must not match 0.0.69 (substring)")
	}
	if versionLineMatches("beacon version 0.0.10 (x) built on y", "0.0.101") {
		t.Error("0.0.101 must not match 0.0.10")
	}
	if versionLineMatches("garbage output", "0.0.1") {
		t.Error("unparseable output should not match")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
