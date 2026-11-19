package edition

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
)

const testKey = "bcn_device_abcd1234_secretpart"

var testSHA = strings.Repeat("ab", 32)

func platform(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestLocateSendsTheDeviceKeyAndReadsTheRelease(t *testing.T) {
	var gotAuth, gotBody, gotPath string
	srv := platform(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		answer(w, 200, map[string]any{
			"version": "1.3.41", "name": "BeaconEnterprise-1.3.41-arm64.pkg",
			"url": "https://release-assets.example/pkg?sig=x", "sha256": strings.ToUpper(testSHA), "size": 74725771,
		})
	})

	mac := Target{OS: "darwin", Arch: "arm64", Format: FormatPkg}
	rel, err := LocateFor(context.Background(), nil, srv.URL+"/", testKey, "", mac)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if gotAuth != "Bearer "+testKey || gotPath != DownloadPath || gotBody != `{"os":"darwin","arch":"arm64","format":"pkg"}` {
		t.Fatalf("request = %q %q %q", gotAuth, gotPath, gotBody)
	}
	want := Release{Version: "1.3.41", Name: "BeaconEnterprise-1.3.41-arm64.pkg",
		URL: "https://release-assets.example/pkg?sig=x", SHA256: testSHA, Size: 74725771}
	if rel != want {
		t.Fatalf("release = %+v", rel)
	}

	if _, err := LocateFor(context.Background(), nil, srv.URL, testKey, "v1.3.41", mac); err != nil {
		t.Fatalf("Locate with a version: %v", err)
	}
	if gotBody != `{"version":"1.3.41","os":"darwin","arch":"arm64","format":"pkg"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestLocateRefusesToSendTheKeyOverPlainHTTP(t *testing.T) {
	_, err := Locate(context.Background(), nil, "http://platform.example", testKey, "")
	if err == nil || !strings.Contains(err.Error(), "https is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocateNeverFollowsARedirect(t *testing.T) {
	var followed atomic.Bool
	elsewhere := platform(t, func(http.ResponseWriter, *http.Request) { followed.Store(true) })
	srv := platform(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+DownloadPath, http.StatusTemporaryRedirect)
	})
	_, err := Locate(context.Background(), nil, srv.URL, testKey, "")
	var refused *HTTPError
	if !errors.As(err, &refused) || refused.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v", err)
	}
	if followed.Load() {
		t.Fatal("the device key followed a redirect")
	}
}

func TestLocateReturnsThePlatformsRefusal(t *testing.T) {
	srv := platform(t, func(w http.ResponseWriter, _ *http.Request) {
		answer(w, 403, map[string]string{"detail": "policy enforcement is not enabled for this organization"})
	})
	_, err := Locate(context.Background(), nil, srv.URL, testKey, "")
	var refused *HTTPError
	if !errors.As(err, &refused) || refused.Status != 403 ||
		refused.Detail != "policy enforcement is not enabled for this organization" {
		t.Fatalf("err = %v", err)
	}
}

func TestLocateRejectsAnAnswerItCannotInstall(t *testing.T) {
	good := map[string]any{"version": "1.3.41", "url": "https://x.example/p", "sha256": testSHA, "size": 1}
	for name, tc := range map[string]struct {
		change  map[string]any
		version string
		want    string
	}{
		"no version":      {change: map[string]any{"version": "latest"}, want: "no release version"},
		"another version": {version: "1.3.40", want: "answered with 1.3.41"},
		"no checksum":     {change: map[string]any{"sha256": "abc"}, want: "no SHA-256"},
		"plain http link": {change: map[string]any{"url": "http://x.example/p"}, want: "no https download link"},
	} {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{}
			for k, v := range good {
				body[k] = v
			}
			for k, v := range tc.change {
				body[k] = v
			}
			srv := platform(t, func(w http.ResponseWriter, _ *http.Request) { answer(w, 200, body) })
			_, err := Locate(context.Background(), nil, srv.URL, testKey, tc.version)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

var linuxDeb = Target{OS: "linux", Arch: "amd64", Format: FormatDeb}

func linuxAnswer() map[string]any {
	return map[string]any{
		"version": "1.3.41", "name": "beacon_1.3.41_linux_amd64.deb",
		"url": "https://release-assets.example/deb?sig=x", "sha256": testSHA, "size": 1,
		"checksums": map[string]any{
			"url":           "https://release-assets.example/sums?sig=x",
			"signature_url": "https://release-assets.example/sig?sig=x",
			"key_url":       "https://release-assets.example/key?sig=x",
		},
	}
}

func TestLocateForLinuxReadsTheSignedChecksumsLinks(t *testing.T) {
	var gotBody string
	srv := platform(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		answer(w, 200, linuxAnswer())
	})
	rel, err := LocateFor(context.Background(), nil, srv.URL, testKey, "", linuxDeb)
	if err != nil {
		t.Fatalf("LocateFor: %v", err)
	}
	if gotBody != `{"os":"linux","arch":"amd64","format":"deb"}` {
		t.Fatalf("body = %q", gotBody)
	}
	want := Checksums{
		URL:          "https://release-assets.example/sums?sig=x",
		SignatureURL: "https://release-assets.example/sig?sig=x",
		KeyURL:       "https://release-assets.example/key?sig=x",
	}
	if rel.Checksums != want {
		t.Fatalf("checksums = %+v", rel.Checksums)
	}
	pkg := rel.pkg()
	if pkg.Name != rel.Name || pkg.Checksums.URL != want.URL || pkg.Checksums.SignatureURL != want.SignatureURL ||
		pkg.Checksums.KeyURL != want.KeyURL || pkg.Edition != Enterprise {
		t.Fatalf("package = %+v", pkg)
	}
}

// A platform that predates Linux packages ignores the target and answers with the macOS package.
func TestLocateForLinuxRefusesAnswersItCannotVerify(t *testing.T) {
	for name, tc := range map[string]struct {
		change map[string]any
		want   string
	}{
		"the macOS package": {change: map[string]any{"name": "BeaconEnterprise-1.3.41-arm64.pkg"}, want: "not a deb package for linux/amd64"},
		"another format":    {change: map[string]any{"name": "beacon_1.3.41_linux_amd64.rpm"}, want: "not a deb package"},
		"a path":            {change: map[string]any{"name": "../beacon_1.3.41_linux_amd64.deb"}, want: "not a deb package"},
		"no checksums":      {change: map[string]any{"checksums": nil}, want: "no https links to the release's signed checksums"},
		"plain http key": {change: map[string]any{"checksums": map[string]any{
			"url": "https://x.example/s", "signature_url": "https://x.example/g", "key_url": "http://x.example/k",
		}}, want: "no https links"},
	} {
		t.Run(name, func(t *testing.T) {
			body := linuxAnswer()
			for k, v := range tc.change {
				body[k] = v
			}
			srv := platform(t, func(w http.ResponseWriter, _ *http.Request) { answer(w, 200, body) })
			_, err := LocateFor(context.Background(), nil, srv.URL, testKey, "", linuxDeb)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTargetsNameThisPlatform(t *testing.T) {
	sys, user := SystemTarget(), UserTarget()
	if sys.OS != runtime.GOOS || sys.Arch != runtime.GOARCH || user.OS != runtime.GOOS || user.Arch != runtime.GOARCH {
		t.Fatalf("targets = %+v, %+v", sys, user)
	}
	if runtime.GOOS == "linux" {
		if user.Format != FormatTarball || (sys.Format != FormatDeb && sys.Format != FormatRPM) {
			t.Fatalf("linux targets = %+v, %+v", sys, user)
		}
	} else if sys.Format != FormatPkg || user.Format != FormatPkg {
		t.Fatalf("targets = %+v, %+v", sys, user)
	}
}

func TestSystemTargetOnLinuxAsksForWhatThisHostInstalls(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native package format is chosen on Linux only")
	}
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	for tools, want := range map[string]string{"dpkg rpm": FormatDeb, "rpm": FormatRPM, "": FormatDeb} {
		lookPath = func(name string) (string, error) {
			if strings.Contains(" "+tools+" ", " "+name+" ") {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
		if got := SystemTarget().Format; got != want {
			t.Fatalf("with %q: format = %s, want %s", tools, got, want)
		}
	}
}

// releaseTarball is a gzipped tar of files, name to content; a name ending in "->target" is a
// symlink.
func releaseTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if link, target, ok := strings.Cut(name, "->"); ok {
			if err := tw.WriteHeader(&tar.Header{Name: link, Typeflag: tar.TypeSymlink, Linkname: target}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stubTarballInstall fakes the verified download of a release tarball: it is written, as the
// self-updater would, once pkg names the tarball and its signed checksums.
func stubTarballInstall(t *testing.T, tarball []byte, line string) *selfupdate.Package {
	t.Helper()
	oldFetch, oldRun := fetchVerifiedPackage, runCommand
	t.Cleanup(func() { fetchVerifiedPackage, runCommand = oldFetch, oldRun })
	var got selfupdate.Package
	fetchVerifiedPackage = func(_ context.Context, pkg selfupdate.Package, dest string) error {
		got = pkg
		return os.WriteFile(dest, tarball, 0o644)
	}
	runCommand = func(_ context.Context, name string, _ ...string) (string, error) {
		if name == "pkgutil" {
			return "", errors.New("pkgutil ran for a tarball")
		}
		data, err := os.ReadFile(name)
		if err != nil || string(data) != "enterprise beacon" {
			return "", fmt.Errorf("ran %s (%q, %v)", name, data, err)
		}
		return line, nil
	}
	return &got
}

var linuxTarballRelease = Release{
	Version: "1.3.41", Name: "beacon-enterprise_1.3.41_linux_amd64.tar.gz", URL: "https://x/t?sig=x", SHA256: testSHA,
	Checksums: Checksums{URL: "https://x/s", SignatureURL: "https://x/g", KeyURL: "https://x/k"},
}

func TestInstallUserTakesBeaconFromTheReleaseTarball(t *testing.T) {
	tarball := releaseTarball(t, map[string]string{
		"./beacon": "enterprise beacon", "beacon-hooks": "hooks", "beacon-otelcol": "collector", "README.md": "readme",
	})
	got := stubTarballInstall(t, tarball, "beacon version 1.3.41 (abc) built on today, enterprise edition\n")
	dest := filepath.Join(t.TempDir(), "enterprise", "beacon")
	if err := InstallUser(context.Background(), linuxTarballRelease, dest); err != nil {
		t.Fatalf("InstallUser: %v", err)
	}
	if got.Name != linuxTarballRelease.Name || got.Checksums.URL != "https://x/s" || got.Checksums.KeyURL != "https://x/k" {
		t.Fatalf("verified package = %+v", *got)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "enterprise beacon" {
		t.Fatalf("dest = %q, %v", data, err)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Fatalf("left behind %v, want only the binary", entries)
	}
}

func TestInstallUserRefusesATarballWithoutOneBeaconAtItsRoot(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"none":      {"beacon-hooks": "hooks"},
		"nested":    {"bin/beacon": "enterprise beacon"},
		"a symlink": {"beacon->/bin/sh": ""},
		"outside":   {"../beacon": "enterprise beacon"},
		"twice":     {"beacon": "enterprise beacon", "./beacon": "enterprise beacon"},
	} {
		t.Run(name, func(t *testing.T) {
			stubTarballInstall(t, releaseTarball(t, files), "beacon version 1.3.41 (abc) built on today, enterprise edition\n")
			dest := filepath.Join(t.TempDir(), "enterprise", "beacon")
			if err := InstallUser(context.Background(), linuxTarballRelease, dest); err == nil {
				t.Fatal("installed")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("dest was written (%v)", err)
			}
		})
	}
}

func TestInstallUserRefusesATarballsCommunityBeacon(t *testing.T) {
	stubTarballInstall(t, releaseTarball(t, map[string]string{"beacon": "enterprise beacon"}), "beacon version 1.3.41 (abc) built on today\n")
	dest := filepath.Join(t.TempDir(), "enterprise", "beacon")
	err := InstallUser(context.Background(), linuxTarballRelease, dest)
	if err == nil || !strings.Contains(err.Error(), "enterprise edition") {
		t.Fatalf("err = %v", err)
	}
}

// stubUserInstall fakes the verified download and pkgutil: the expanded package holds a beacon
// that reports line.
func stubUserInstall(t *testing.T, line string, fetchErr error) {
	t.Helper()
	oldFetch, oldRun := fetchVerifiedPackage, runCommand
	t.Cleanup(func() { fetchVerifiedPackage, runCommand = oldFetch, oldRun })
	fetchVerifiedPackage = func(_ context.Context, _ selfupdate.Package, dest string) error {
		if fetchErr != nil {
			return fetchErr
		}
		return os.WriteFile(dest, []byte("pkg"), 0o644)
	}
	runCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if name == "pkgutil" {
			bin := filepath.Join(args[len(args)-1], "Payload", "opt", "beacon", "bin", "beacon")
			if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
				return "", err
			}
			return "", os.WriteFile(bin, []byte("enterprise beacon"), 0o644)
		}
		return line, nil // <bin> version
	}
}

func TestInstallUserKeepsOnlyThePackagesBeacon(t *testing.T) {
	stubUserInstall(t, "beacon version 1.3.41 (abc) built on today, enterprise edition\n", nil)
	dest := filepath.Join(t.TempDir(), "enterprise", "beacon")
	if err := InstallUser(context.Background(), Release{Version: "1.3.41", URL: "https://x/p", SHA256: testSHA}, dest); err != nil {
		t.Fatalf("InstallUser: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("dest: %v, %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Fatalf("left behind %v, want only the binary", entries)
	}
}

func TestInstallUserRefusesWhatItDidNotAskFor(t *testing.T) {
	cases := map[string]struct {
		line     string
		fetchErr error
		want     string
	}{
		"community edition": {"beacon version 1.3.41 (abc) built on today\n", nil, "enterprise edition"},
		"other version":     {"beacon version 1.3.40 (abc) built on today, enterprise edition\n", nil, "want 1.3.41"},
		"failed check":      {"", errors.New("verify checksum: sha256 mismatch"), "sha256 mismatch"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stubUserInstall(t, tc.line, tc.fetchErr)
			dest := filepath.Join(t.TempDir(), "enterprise", "beacon")
			err := InstallUser(context.Background(), Release{Version: "1.3.41", URL: "https://x/p", SHA256: testSHA}, dest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("dest was written (%v)", err)
			}
		})
	}
}

func TestLinuxRollbackPackageIsThePublicPackageInstalledNow(t *testing.T) {
	old := installedVersionLine
	t.Cleanup(func() { installedVersionLine = old })
	rpm := Release{Name: "beacon-enterprise_1.3.41_linux_amd64.rpm"}
	deb := Release{Name: "beacon-enterprise_1.3.41_linux_amd64.deb"}

	installedVersionLine = func(context.Context) string { return "beacon version 1.3.40 (abc) built on today\n" }
	for rel, ext := range map[Release]string{rpm: "rpm", deb: "deb"} {
		got, err := linuxRollbackPackage(context.Background(), rel)
		name := "beacon_1.3.40_linux_" + runtime.GOARCH + "." + ext
		if err != nil || got == nil || got.Version != "1.3.40" || got.Name != name || got.SHA256 != "" ||
			got.URL != "https://github.com/asymptote-labs/agent-beacon/releases/download/v1.3.40/"+name {
			t.Fatalf("%s: %+v, %v", ext, got, err)
		}
	}

	installedVersionLine = func(context.Context) string {
		return "beacon version 1.3.40 (abc) built on today, enterprise edition\n"
	}
	if got, err := linuxRollbackPackage(context.Background(), rpm); got != nil || err != nil {
		t.Fatalf("over the enterprise edition: %+v, %v", got, err)
	}

	for _, line := range []string{"", "beacon version dev (unknown) built on unknown\n"} {
		installedVersionLine = func(context.Context) string { return line }
		if _, err := linuxRollbackPackage(context.Background(), deb); err == nil || !strings.Contains(err.Error(), "not a public release") {
			t.Fatalf("installed %q: err = %v", line, err)
		}
	}
}
