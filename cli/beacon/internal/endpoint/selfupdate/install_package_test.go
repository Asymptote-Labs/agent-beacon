package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// packageServer serves one artifact at /artifact.tar.gz and counts the downloads.
func packageServer(t *testing.T, artifact []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifact.tar.gz" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write(artifact)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func packageApplier(t *testing.T, current string) *Applier {
	t.Helper()
	a := NewApplier(current)
	a.ManifestURL = "http://127.0.0.1:1/never-fetched.json"
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.AllowInsecureTest = true
	a.SkipGatekeeper = true
	a.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	return a
}

func installedVersionLine(t *testing.T, a *Applier) string {
	t.Helper()
	out, err := execRun(context.Background(), filepath.Join(a.binDir(), "beacon"), "version")
	if err != nil {
		t.Fatalf("run installed beacon: %v: %s", err, out)
	}
	return strings.TrimSpace(out)
}

func skipWithoutShellStubs(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX install flow stages an executable shell stub; the Windows flow is not implemented yet")
	}
}

func TestInstallPackageInstallsTheSameVersionWithoutAManifest(t *testing.T) {
	skipWithoutShellStubs(t)
	artifact, sha := makeBeaconTarball(t, "1.3.41", "partner")
	srv, hits := packageServer(t, artifact)

	// The running version equals the package's: Apply would stop at "already up to date".
	a := packageApplier(t, "1.3.41")
	res, err := a.InstallPackage(context.Background(), Package{
		URL: srv.URL + "/artifact.tar.gz", SHA256: sha, Version: "1.3.41", Edition: "partner",
	})
	if err != nil {
		t.Fatalf("InstallPackage: %v", err)
	}
	if !res.Applied || res.RolledBack || res.FromVersion != "1.3.41" || res.ToVersion != "1.3.41" {
		t.Fatalf("result = %+v", res)
	}
	if res.Message != "installed 1.3.41, partner edition" {
		t.Fatalf("message = %q", res.Message)
	}
	if hits.Load() != 1 {
		t.Fatalf("downloads = %d", hits.Load())
	}
	if got := installedVersionLine(t, a); got != "beacon version 1.3.41 (test) built on test, partner edition" {
		t.Fatalf("installed = %q", got)
	}
	log, err := os.ReadFile(a.LogPath)
	if err != nil || !strings.Contains(string(log), "update.applied") {
		t.Fatalf("telemetry = %q, %v", log, err)
	}
}

func TestInstallPackageRollsBackWhenTheEditionIsWrong(t *testing.T) {
	skipWithoutShellStubs(t)
	a := packageApplier(t, "1.3.40")
	before, beforeSHA := makeBeaconTarball(t, "1.3.40")
	srv, _ := packageServer(t, before)
	if _, err := a.InstallPackage(context.Background(), Package{
		URL: srv.URL + "/artifact.tar.gz", SHA256: beforeSHA, Version: "1.3.40",
	}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// A package that installs a community build where the partner edition was expected.
	wrong, wrongSHA := makeBeaconTarball(t, "1.3.41")
	srv2, _ := packageServer(t, wrong)
	res, err := a.InstallPackage(context.Background(), Package{
		URL: srv2.URL + "/artifact.tar.gz", SHA256: wrongSHA, Version: "1.3.41", Edition: "partner",
	})
	if err == nil || !strings.Contains(err.Error(), "expected the partner edition") {
		t.Fatalf("err = %v", err)
	}
	if !res.RolledBack || res.Applied {
		t.Fatalf("result = %+v", res)
	}
	if got := installedVersionLine(t, a); got != "beacon version 1.3.40 (test) built on test" {
		t.Fatalf("after rollback = %q", got)
	}
}

func TestInstallPackageChecksumMismatchInstallsNothing(t *testing.T) {
	skipWithoutShellStubs(t)
	artifact, _ := makeBeaconTarball(t, "1.3.41")
	srv, _ := packageServer(t, artifact)
	a := packageApplier(t, "1.3.40")
	_, err := a.InstallPackage(context.Background(), Package{
		URL: srv.URL + "/artifact.tar.gz", SHA256: strings.Repeat("0", 64), Version: "1.3.41",
	})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(a.binDir(), "beacon")); !os.IsNotExist(statErr) {
		t.Fatalf("a package that failed its checksum was installed: %v", statErr)
	}
}

func TestInstallPackageRefusesBeforeDownloading(t *testing.T) {
	artifact, sha := makeBeaconTarball(t, "1.3.41")
	srv, hits := packageServer(t, artifact)

	cases := map[string]struct {
		insecure bool
		pkg      Package
		want     string
	}{
		"plain http": {
			pkg:  Package{URL: "http://updates.example.com/artifact.tar.gz", SHA256: sha, Version: "1.3.41"},
			want: "non-https",
		},
		"not a release version": {
			insecure: true,
			pkg:      Package{URL: srv.URL + "/artifact.tar.gz", SHA256: sha, Version: "latest"},
			want:     "not a release version",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := packageApplier(t, "1.3.40")
			a.AllowInsecureTest, a.SkipGatekeeper = tc.insecure, tc.insecure
			_, err := a.InstallPackage(context.Background(), tc.pkg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("downloads = %d, want none", hits.Load())
	}
}

func TestInstallPackageRequiresBeaconsSigningTeam(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Gatekeeper verification is macOS-only")
	}
	artifact, sha := makeBeaconTarball(t, "1.3.41")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer srv.Close()

	a := NewApplier("1.3.40")
	a.HTTPClient = srv.Client()
	a.StageDir = t.TempDir()
	a.InstallPrefix = t.TempDir()
	a.LogPath = filepath.Join(t.TempDir(), "runtime.jsonl")
	var calls []string
	a.run = func(_ context.Context, name string, _ ...string) (string, error) {
		calls = append(calls, name)
		if name == "pkgutil" {
			return "1. Developer ID Installer: Someone Else (OTHERTEAM1)", nil
		}
		return "", fmt.Errorf("%s must not run", name)
	}
	_, err := a.InstallPackage(context.Background(), Package{
		URL: srv.URL + "/artifact.pkg", SHA256: sha, Version: "1.3.41",
	})
	if err == nil || !strings.Contains(err.Error(), "want "+AppleTeamID) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(calls, ",") != "pkgutil" {
		t.Fatalf("calls = %v; nothing may run after a signature from another team", calls)
	}
}

func TestVersionLineEdition(t *testing.T) {
	for out, want := range map[string]string{
		"beacon version 1.3.41 (abc1234) built on 2026-10-15\n":                     "",
		"beacon version 1.3.41 (abc1234) built on 2026-10-15, enterprise edition\n": "enterprise",
		"beacon version 1.3.41 (abc) built on x, partner edition\nextra line":       "partner",
		"beacon version dev (unknown) built on unknown":                             "",
		"":                "",
		"special edition": "",
	} {
		if got := VersionLineEdition(out); got != want {
			t.Errorf("VersionLineEdition(%q) = %q, want %q", out, got, want)
		}
	}
}
