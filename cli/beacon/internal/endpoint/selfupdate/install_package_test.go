package selfupdate

import (
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
	a.run = runStubBinary
	return a
}

func installedVersionLine(t *testing.T, a *Applier) string {
	t.Helper()
	out, err := runStubBinary(context.Background(), filepath.Join(a.binDir(), "beacon"), "version")
	if err != nil {
		t.Fatalf("run installed beacon: %v: %s", err, out)
	}
	return strings.TrimSpace(out)
}

func TestInstallPackageInstallsTheSameVersionWithoutAManifest(t *testing.T) {
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

// rollbackServer serves the package of what is installed now at /previous.deb, as the public
// release would, and counts the downloads.
func rollbackServer(t *testing.T, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/previous.deb" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A failed install with RollbackPackage reinstalls the previous package through the package
// manager, so its database names what is on disk again, and copies the files back.
func TestInstallPackageRollsBackThroughThePackageManager(t *testing.T) {
	a := packageApplier(t, "1.3.40")
	before, beforeSHA := makeBeaconTarball(t, "1.3.40")
	srv, _ := packageServer(t, before)
	if _, err := a.InstallPackage(context.Background(), Package{URL: srv.URL + "/artifact.tar.gz", SHA256: beforeSHA, Version: "1.3.40"}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	previous := []byte("the public 1.3.40 package")
	sum := sha256.Sum256(previous)
	prev, _ := rollbackServer(t, previous)
	var reinstalled []string
	a.run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "dpkg" {
			data, err := os.ReadFile(args[len(args)-1])
			if err != nil || string(data) != string(previous) {
				return "", fmt.Errorf("dpkg was given %q (%v)", data, err)
			}
			reinstalled = append(reinstalled, name+" "+strings.Join(args[:len(args)-1], " "))
			return "", nil
		}
		return runStubBinary(ctx, name, args...)
	}
	a.RollbackPackage = &Package{URL: prev.URL + "/previous.deb", SHA256: hex.EncodeToString(sum[:]), Version: "1.3.40", Name: "beacon_1.3.40_linux_amd64.deb"}

	wrong, wrongSHA := makeBeaconTarball(t, "1.3.41")
	srv2, _ := packageServer(t, wrong)
	res, err := a.InstallPackage(context.Background(), Package{URL: srv2.URL + "/artifact.tar.gz", SHA256: wrongSHA, Version: "1.3.41", Edition: "enterprise"})
	if err == nil || !strings.Contains(err.Error(), "expected the enterprise edition") {
		t.Fatalf("err = %v", err)
	}
	if !res.RolledBack || strings.Join(reinstalled, ",") != "dpkg --install" {
		t.Fatalf("result = %+v, reinstalled %v", res, reinstalled)
	}
	if got := installedVersionLine(t, a); got != "beacon version 1.3.40 (test) built on test" {
		t.Fatalf("after rollback = %q", got)
	}
}

// Without the package a rollback would reinstall, nothing is installed.
func TestInstallPackageRefusesWithoutItsRollbackPackage(t *testing.T) {
	a := packageApplier(t, "1.3.40")
	before, beforeSHA := makeBeaconTarball(t, "1.3.40")
	srv, _ := packageServer(t, before)
	if _, err := a.InstallPackage(context.Background(), Package{URL: srv.URL + "/artifact.tar.gz", SHA256: beforeSHA, Version: "1.3.40"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	prev, _ := rollbackServer(t, []byte("the public 1.3.40 package"))
	for name, pkg := range map[string]Package{
		"not published":  {URL: prev.URL + "/missing.deb", Version: "1.3.40"},
		"wrong checksum": {URL: prev.URL + "/previous.deb", SHA256: strings.Repeat("0", 64), Version: "1.3.40"},
	} {
		t.Run(name, func(t *testing.T) {
			a.RollbackPackage = &pkg
			next, nextSHA := makeBeaconTarball(t, "1.3.41", "enterprise")
			srv2, hits := packageServer(t, next)
			_, err := a.InstallPackage(context.Background(), Package{URL: srv2.URL + "/artifact.tar.gz", SHA256: nextSHA, Version: "1.3.41", Edition: "enterprise"})
			if err == nil || !strings.Contains(err.Error(), "which a failed install reinstalls") {
				t.Fatalf("err = %v", err)
			}
			if hits.Load() != 1 {
				t.Fatalf("downloads = %d", hits.Load())
			}
			if got := installedVersionLine(t, a); got != "beacon version 1.3.40 (test) built on test" {
				t.Fatalf("installed = %q, want the install untouched", got)
			}
		})
	}
}

// rpm will not install a package that ranks below the installed one without --oldpackage, and the
// previous package does whenever the failed one carries a higher epoch.
func TestReinstallTakesTheOlderPackage(t *testing.T) {
	for path, want := range map[string]string{
		"/stage/rollback-package.rpm": "rpm --upgrade --oldpackage --replacepkgs /stage/rollback-package.rpm",
		"/stage/rollback-package.deb": "dpkg --install /stage/rollback-package.deb",
	} {
		var ran string
		a := &Applier{run: func(_ context.Context, name string, args ...string) (string, error) {
			ran = name + " " + strings.Join(args, " ")
			return "", nil
		}}
		if err := a.reinstall(context.Background(), path); err != nil || ran != want {
			t.Fatalf("%s: ran %q (%v), want %q", path, ran, err, want)
		}
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
