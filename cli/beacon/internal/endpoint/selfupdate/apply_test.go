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
	"runtime"
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
	return manifestServerNamed(t, version, sha, artifact, "/artifact.tar.gz")
}

// nativeManifestServer publishes the artifact under a name and key this host would really install,
// for the tests that exercise the production install path rather than the tarball seam.
//
// On Linux the artifact key carries the package format, because .deb and .rpm hosts share an
// architecture. A tarball name would resolve to no artifact at all there, so a test written for
// macOS's `installer` would fail before reaching what it meant to check.
func nativeManifestServer(t *testing.T, version, sha string, artifact []byte) *httptest.Server {
	t.Helper()
	if runtime.GOOS != "linux" {
		return manifestServerNamed(t, version, sha, artifact, "/artifact.pkg")
	}
	ext := linuxPackageExt()
	return manifestServerNamed(t, version, sha, artifact, "/artifact"+ext,
		updatecheck.RuntimeArchKey()+"_"+strings.TrimPrefix(ext, "."))
}

// manifestServerNamed serves one artifact at the given path, keyed by archKey (defaulting to this
// platform's).
func manifestServerNamed(t *testing.T, version, sha string, artifact []byte, artifactPath string,
	archKey ...string) *httptest.Server {
	t.Helper()
	key := updatecheck.RuntimeArchKey()
	if len(archKey) > 0 {
		key = archKey[0]
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"schema":1,"version":%q,"team_id":"TEAMID","artifacts":{%q:{"url":%q,"sha256":%q}}}`,
			version, key, srv.URL+artifactPath, sha)
	})
	mux.HandleFunc(artifactPath, func(w http.ResponseWriter, r *http.Request) {
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

func TestApplyCleansSuccessfulUpdateStaging(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := testApplier(t, "0.0.1", srv)
	oldBin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if err := os.MkdirAll(filepath.Dir(oldBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldBin, []byte("#!/bin/sh\necho \"beacon version 0.0.1 (old) built on test\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	staleDownload := filepath.Join(a.StageDir, "download.pkg")
	if err := os.WriteFile(staleDownload, []byte("stale package"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleRollback := filepath.Join(a.StageDir, "rollback", "beacon", "bin", "old")
	if err := os.MkdirAll(filepath.Dir(staleRollback), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleRollback, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staleFailure := filepath.Join(a.StageDir, "last_failure.json")
	if err := os.WriteFile(staleFailure, []byte(`{"reason":"old failure"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := a.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied {
		t.Fatalf("expected update applied: %+v", res)
	}
	for _, path := range []string{
		staleDownload,
		filepath.Join(a.StageDir, "download.tar.gz"),
		filepath.Join(a.StageDir, "rollback"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or unexpected error: %v", path, err)
		}
	}
	if _, err := os.Stat(staleFailure); !os.IsNotExist(err) {
		t.Fatalf("stale failure diagnostic still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.StageDir, ".update.lock")); err != nil {
		t.Fatalf("lock file should remain reusable: %v", err)
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
	if _, err := os.Stat(filepath.Join(a.StageDir, "rollback")); !os.IsNotExist(err) {
		t.Fatalf("successful rollback should clean rollback snapshot, stat err=%v", err)
	}
	diag := string(mustRead(t, filepath.Join(a.StageDir, "last_failure.json")))
	for _, want := range []string{
		`"from_version": "0.0.1"`,
		`"to_version": "9.9.9"`,
		`"rolled_back": true`,
		"post-install health check failed",
	} {
		if !strings.Contains(diag, want) {
			t.Fatalf("failure diagnostic missing %q:\n%s", want, diag)
		}
	}
}

func TestApplyGatekeeperAbortsBeforeInstall(t *testing.T) {
	if runtime.GOOS != "darwin" {
		// Gatekeeper is an Apple mechanism, and verifyGatekeeper is skipped elsewhere by design, so
		// there is no signature step for an installer to run after. The non-Darwin contract --
		// checksum enforcement still aborts before any installer -- is asserted separately in
		// TestApplyChecksumMismatchAbortsBeforeInstall.
		t.Skip("Gatekeeper verification is macOS-only")
	}
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

func TestVerifyGatekeeperSkipsUnavailableStapler(t *testing.T) {
	a := NewApplier("0.0.1")
	var calls []string
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name)
		switch name {
		case "pkgutil":
			return "Developer ID Installer: Example (TEAMID)", nil
		case "stapler":
			return "xcode-select: error: tool 'stapler' requires Xcode, but active developer directory '/Library/Developer/CommandLineTools' is a command line tools instance", fmt.Errorf("exit status 1")
		case "spctl":
			return "accepted", nil
		default:
			return "", nil
		}
	}

	if err := a.verifyGatekeeper(context.Background(), filepath.Join(t.TempDir(), "Beacon.pkg"), "TEAMID"); err != nil {
		t.Fatalf("verifyGatekeeper should skip unavailable stapler, got %v", err)
	}
	if strings.Join(calls, ",") != "pkgutil,stapler,spctl" {
		t.Fatalf("calls = %v, want pkgutil, stapler, spctl", calls)
	}
}

func TestVerifyGatekeeperFailsStaplerValidationFailure(t *testing.T) {
	a := NewApplier("0.0.1")
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		switch name {
		case "pkgutil":
			return "Developer ID Installer: Example (TEAMID)", nil
		case "stapler":
			return "The validate action worked! The staple and validate action failed!", fmt.Errorf("exit status 65")
		default:
			return "", nil
		}
	}

	err := a.verifyGatekeeper(context.Background(), filepath.Join(t.TempDir(), "Beacon.pkg"), "TEAMID")
	if err == nil || !strings.Contains(err.Error(), "stapler validate") {
		t.Fatalf("verifyGatekeeper error = %v, want stapler validation failure", err)
	}
}

func TestApplyRestartsCollectorBeforeHealthCheck(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	// The production install path, not the extraction seam -- the stubbed runner below stands in
	// for the platform's installer. So the artifact has to be named like one this host installs.
	srv := nativeManifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := NewApplier("0.0.1")
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "system.jsonl")
	oldBin := filepath.Join(a.InstallPrefix, "opt/beacon/bin/beacon")
	if err := os.MkdirAll(filepath.Dir(oldBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldBin, []byte("#!/bin/sh\necho \"beacon version 0.0.1 (old) built on test\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		switch filepath.Base(name) {
		case "pkgutil":
			return "Developer ID Installer: Example (TEAMID)", nil
		case "stapler", "spctl", "installer", "dpkg", "rpm":
			return "", nil
		case "beacon":
			return "beacon version 9.9.9 (new) built on test", nil
		default:
			return "", nil
		}
	}
	restartErr := fmt.Errorf("launchctl failed")
	a.restart = func() error { return restartErr }

	res, err := a.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "collector restart failed") {
		t.Fatalf("Apply error = %v, want collector restart failure", err)
	}
	if !res.RolledBack {
		t.Fatalf("filesystem rollback should be recorded after restart failure: %+v", res)
	}
	if !strings.Contains(string(mustRead(t, a.LogPath)), "rollback failed") {
		t.Fatalf("expected rollback restart failure telemetry, got %s", mustRead(t, a.LogPath))
	}
	if _, err := os.Stat(filepath.Join(a.StageDir, "rollback")); err != nil {
		t.Fatalf("failed rollback should preserve rollback materials: %v", err)
	}
	diag := string(mustRead(t, filepath.Join(a.StageDir, "last_failure.json")))
	if !strings.Contains(diag, "rollback_error") || !strings.Contains(diag, "launchctl failed") {
		t.Fatalf("failure diagnostic should preserve rollback error, got %s", diag)
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

// The installer is chosen by artifact suffix, and getting this wrong is easy: filepath.Ext reports
// ".gz" for a .tar.gz, so an Ext-based switch silently fails to match the tarball the test seam
// uses. It also has to leave every non-Linux format on exactly the path it took before Linux
// support existed, or this becomes a macOS behaviour change disguised as a Linux feature.
func TestInstallerCommandDispatchesBySuffix(t *testing.T) {
	cases := []struct {
		path     string
		wantName string
		wantArg  string
	}{
		{"/stage/beacon_1.0.6_linux_amd64.deb", "dpkg", "--install"},
		{"/stage/beacon_1.0.6_linux_arm64.rpm", "rpm", "--upgrade"},
		{"/stage/BeaconEndpointAgent-1.0.6-arm64.pkg", "installer", "-pkg"},
		// The test seam stages a tarball. It must keep reaching the previous installer path.
		{"/stage/beacon_1.0.6_darwin_arm64.tar.gz", "installer", "-pkg"},
		{"/stage/beacon.tgz", "installer", "-pkg"},
	}
	for _, c := range cases {
		name, args := installerCommand(c.path, "/")
		if name != c.wantName {
			t.Errorf("%s -> %q, want %q", c.path, name, c.wantName)
			continue
		}
		if len(args) == 0 || args[0] != c.wantArg {
			t.Errorf("%s -> args %v, want first arg %q", c.path, args, c.wantArg)
		}
		// The artifact must be passed through, or the installer would act on nothing.
		found := false
		for _, a := range args {
			if a == c.path {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the artifact path is missing from args %v", c.path, args)
		}
	}
}

// dpkg and rpm are invoked directly rather than through apt or dnf. The artifact is already
// downloaded and checksum-verified, so there is no repository to consult, and the higher-level
// tools would add a resolution step that can prompt or reach the network. An unattended update
// must not become interactive.
func TestLinuxInstallersUpgradeInPlaceAndDoNotUseAptOrDnf(t *testing.T) {
	for _, path := range []string{"/s/x.deb", "/s/x.rpm"} {
		name, args := installerCommand(path, "/")
		if name == "apt" || name == "apt-get" || name == "dnf" || name == "yum" {
			t.Errorf("%s uses %q; a local verified artifact needs no repository resolution", path, name)
		}
		joined := name + " " + strings.Join(args, " ")
		// Upgrade rather than fresh install, so the running version is replaced not conflicted.
		if !strings.Contains(joined, "--install") && !strings.Contains(joined, "--upgrade") {
			t.Errorf("%s -> %q does not upgrade in place", path, joined)
		}
	}
}

// Verification differs by platform and must be recorded rather than implied, so a weaker check is
// visible in telemetry instead of being indistinguishable from the stronger one.
func TestPackageExtPrefersTheURLThenThePlatform(t *testing.T) {
	cases := map[string]string{
		"https://x/beacon_1.0.6_linux_amd64.deb": ".deb",
		"https://x/beacon_1.0.6_linux_arm64.rpm": ".rpm",
		"https://x/beacon.tar.gz":                ".tar.gz",
		"https://x/beacon.tgz":                   ".tgz",
	}
	for url, want := range cases {
		if got := packageExt(url); got != want {
			t.Errorf("packageExt(%q) = %q, want %q", url, got, want)
		}
	}
	// A URL with no recognised suffix falls back to the platform's native format.
	got := packageExt("https://x/beacon-release")
	if runtime.GOOS == "darwin" && got != ".pkg" {
		t.Errorf("on darwin the fallback should be .pkg, got %q", got)
	}
	if runtime.GOOS == "linux" && got != ".deb" && got != ".rpm" {
		t.Errorf("on linux the fallback should be a native package format, got %q", got)
	}
}

// Two package formats share one architecture on Linux, so an architecture key alone cannot say
// which artifact a host can install. These pin the behavior that keeps a Fedora host from
// downloading, verifying, and then failing to install a .deb.
func TestSelectArtifactPrefersTheFormatQualifiedKey(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("format-qualified keys only exist on Linux")
	}
	arch := updatecheck.RuntimeArchKey()
	format := strings.TrimPrefix(linuxPackageExt(), ".")
	m := updatecheck.UpdateManifest{Artifacts: map[string]updatecheck.Artifact{
		arch:                    {URL: "https://x/wrong.tar.gz", SHA256: strings.Repeat("a", 64)},
		arch + "_" + format:     {URL: "https://x/right." + format, SHA256: strings.Repeat("b", 64)},
		arch + "_somethingelse": {URL: "https://x/other.pkg", SHA256: strings.Repeat("c", 64)},
	}}
	got, key, ok := selectArtifact(m, false)
	if !ok {
		t.Fatal("a manifest with a format-qualified key must resolve")
	}
	if key != arch+"_"+format {
		t.Errorf("key = %q, want the format-qualified one", key)
	}
	if !strings.HasSuffix(got.URL, "."+format) {
		t.Errorf("URL = %q, want the artifact for this host's package format", got.URL)
	}
}

func TestSelectArtifactRefusesAForeignPackageFormat(t *testing.T) {
	if runtime.GOOS != "linux" || !hasPackageManager() {
		t.Skip("needs a Linux host with a package manager")
	}
	// Whichever format this host cannot install.
	foreign := ".rpm"
	if linuxPackageExt() == ".rpm" {
		foreign = ".deb"
	}
	m := updatecheck.UpdateManifest{Artifacts: map[string]updatecheck.Artifact{
		updatecheck.RuntimeArchKey(): {URL: "https://x/pkg" + foreign, SHA256: strings.Repeat("a", 64)},
	}}
	if _, key, ok := selectArtifact(m, false); ok {
		t.Errorf("resolved a %s artifact on a host that cannot install one", foreign)
	} else if !strings.Contains(key, strings.TrimPrefix(linuxPackageExt(), ".")) {
		t.Errorf("the refusal names %q; it should name the format this host wanted", key)
	}
}

// A tarball under the architecture key must be refused, not accepted. `install` extracts a tarball
// only under the test seam; in production it goes to the macOS `installer`, which does not exist on
// Linux. Refusing here means the failure arrives before a download rather than after one.
func TestSelectArtifactRefusesATarballOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only fallback")
	}
	m := updatecheck.UpdateManifest{Artifacts: map[string]updatecheck.Artifact{
		updatecheck.RuntimeArchKey(): {URL: "https://x/beacon.tar.gz", SHA256: strings.Repeat("a", 64)},
	}}
	if _, _, ok := selectArtifact(m, false); ok {
		t.Error("a tarball is not installable in production and must not resolve")
	}
	// With the extraction seam on, the same artifact is installable -- which is why the answer is
	// threaded through rather than decided from the host alone.
	if _, _, ok := selectArtifact(m, true); !ok {
		t.Error("a tarball must resolve when the caller extracts tarballs")
	}
}

func TestSelectArtifactOnDarwinUsesTheArchKeyUnchanged(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only behavior")
	}
	arch := updatecheck.RuntimeArchKey()
	m := updatecheck.UpdateManifest{Artifacts: map[string]updatecheck.Artifact{
		arch: {URL: "https://x/BeaconEndpointAgent.pkg", SHA256: strings.Repeat("a", 64)},
	}}
	got, key, ok := selectArtifact(m, false)
	if !ok || key != arch || !strings.HasSuffix(got.URL, ".pkg") {
		t.Fatalf("selectArtifact = %+v, %q, %v; macOS must be unchanged", got, key, ok)
	}
}

// The platform-independent half of the guarantee TestApplyGatekeeperAbortsBeforeInstall makes on
// macOS: nothing is installed unless the bytes matched. Gatekeeper adds assurance on macOS and has
// no Linux equivalent, but SHA-256 is enforced everywhere, and this asserts it on every platform
// rather than leaving Linux with no coverage of "verify before install".
func TestApplyChecksumMismatchAbortsBeforeInstall(t *testing.T) {
	artifact, _ := makeBeaconTarball(t, "9.9.9")
	// A syntactically valid checksum that does not match the artifact.
	srv := manifestServer(t, "9.9.9", strings.Repeat("0", 64), artifact)
	defer srv.Close()

	a := NewApplier("0.0.1")
	a.ManifestURL = srv.URL + "/manifest.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	a.SkipGatekeeper = true // isolate the checksum: this test is not about signatures
	var calls []string
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name)
		return "", nil
	}

	if _, err := a.Apply(context.Background()); err == nil {
		t.Fatal("a checksum mismatch must fail the update")
	}
	if len(calls) != 0 {
		t.Errorf("nothing may run after a checksum mismatch; calls=%v", calls)
	}
}

// Verification exists so an operator can tell a notarization-verified macOS update from a
// checksum-only Linux one. It was computed into the result and never written into the event, so both
// looked identical in the system log -- the field was decorative.
func TestApplyRecordsVerificationInTelemetry(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "9.9.9")
	srv := manifestServer(t, "9.9.9", sha, artifact)
	defer srv.Close()

	a := testApplier(t, "0.0.1", srv)
	a.restart = func() error { return nil }
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "beacon" {
			return "beacon version 9.9.9 (new) built on test", nil
		}
		return "", nil
	}

	res, err := a.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Verification == "" {
		t.Fatal("every applied update must name the checks the artifact passed")
	}
	log := string(mustRead(t, a.LogPath))
	if !strings.Contains(log, `"verification"`) || !strings.Contains(log, res.Verification) {
		t.Errorf("verification %q is missing from the system log:\n%s", res.Verification, log)
	}
	// The value must be interpretable, not just present.
	if !strings.Contains(res.Verification, "sha256") {
		t.Errorf("verification = %q; SHA-256 is enforced on every platform and should say so",
			res.Verification)
	}
}
