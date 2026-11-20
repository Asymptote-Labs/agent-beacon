package edition

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	windowsMSI = Target{OS: "windows", Arch: "amd64", Format: FormatMSI}
	windowsZip = Target{OS: "windows", Arch: "amd64", Format: FormatZip}
)

func windowsAnswer(name string) map[string]any {
	body := linuxAnswer()
	body["name"] = name
	return body
}

func publicMSIRollback(version string) map[string]any {
	b := rollbackAnswer(version)
	b["name"] = "BeaconEndpointAgent-" + version + "-x64.msi"
	return b
}

// Over the public edition, the MSI install asks for the public MSI of the installed version too,
// which a failed install reinstalls.
func TestLocateForWindowsAsksForTheMSIAndThePublicOneToRollBackTo(t *testing.T) {
	installed(t, "beacon version 1.3.40 (abc) built on today\n")
	var gotBody string
	srv := platform(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		body := windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.msi")
		body["rollback"] = publicMSIRollback("1.3.40")
		answer(w, 200, body)
	})
	rel, err := LocateFor(context.Background(), nil, srv.URL, testKey, "", windowsMSI)
	if err != nil {
		t.Fatalf("LocateFor: %v", err)
	}
	if gotBody != `{"os":"windows","arch":"amd64","format":"msi","rollback_from":"1.3.40"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if rel.Name != "beacon-enterprise_1.3.41_windows_amd64.msi" || rel.Rollback == nil || rel.Rollback.Name != "BeaconEndpointAgent-1.3.40-x64.msi" {
		t.Fatalf("release = %+v", rel)
	}
	back, err := rollbackPackage(context.Background(), rel)
	if err != nil || back == nil || back.Name != "BeaconEndpointAgent-1.3.40-x64.msi" || back.Checksums.KeyURL == "" {
		t.Fatalf("rollback package = %+v, %v", back, err)
	}
}

func TestLocateForWindowsRefusesAnswersItCannotVerify(t *testing.T) {
	installed(t, "beacon version 1.3.40 (abc) built on today\n")
	for name, tc := range map[string]struct {
		target Target
		body   map[string]any
		want   string
	}{
		"the macOS package": {windowsMSI, windowsAnswer("BeaconEnterprise-1.3.41-arm64.pkg"), "not a msi package for windows/amd64"},
		"the zip for an MSI": {windowsMSI, func() map[string]any {
			b := windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.zip")
			b["rollback"] = publicMSIRollback("1.3.40")
			return b
		}(), "not a msi package"},
		"no checksums": {windowsZip, func() map[string]any {
			b := windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.zip")
			b["checksums"] = nil
			return b
		}(), "no https links to the release's signed checksums"},
		"no rollback": {windowsMSI, windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.msi"), "no public msi package of 1.3.40"},
		"the Linux public package to roll back to": {windowsMSI, func() map[string]any {
			b := windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.msi")
			b["rollback"] = rollbackAnswer("1.3.40")
			return b
		}(), `answered with "beacon_1.3.40_linux_amd64.deb"`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := platform(t, func(w http.ResponseWriter, _ *http.Request) { answer(w, 200, tc.body) })
			_, err := LocateFor(context.Background(), nil, srv.URL, testKey, "", tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// The user-mode zip is not installed by a package manager, so no rollback is asked for.
func TestLocateForWindowsZipAsksNoRollback(t *testing.T) {
	installed(t, "beacon version 1.3.40 (abc) built on today\n")
	var gotBody string
	srv := platform(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		answer(w, 200, windowsAnswer("beacon-enterprise_1.3.41_windows_amd64.zip"))
	})
	if _, err := LocateFor(context.Background(), nil, srv.URL, testKey, "", windowsZip); err != nil {
		t.Fatalf("LocateFor: %v", err)
	}
	if gotBody != `{"os":"windows","arch":"amd64","format":"zip"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

// zipEntry is one file of a test zip; a mode with os.ModeSymlink makes a link.
type zipEntry struct {
	name, content string
	mode          os.FileMode
}

func releaseZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var windowsZipRelease = Release{
	Version: "1.3.41", Name: "beacon-enterprise_1.3.41_windows_amd64.zip", URL: "https://x/z?sig=x", SHA256: testSHA,
	Checksums: Checksums{URL: "https://x/s", SignatureURL: "https://x/g", KeyURL: "https://x/k"},
}

// The public Windows archive's layout: the binaries at the root, beside a README.
func publicLayoutZip(t *testing.T) []byte {
	return releaseZip(t,
		zipEntry{name: "beacon.exe", content: "enterprise beacon"},
		zipEntry{name: "beacon-hooks.exe", content: "hooks"},
		zipEntry{name: "beacon-otelcol.exe", content: "collector"},
		zipEntry{name: "README.md", content: "readme", mode: 0o644},
	)
}

func TestInstallUserTakesBeaconExeFromTheReleaseZip(t *testing.T) {
	got := stubTarballInstall(t, publicLayoutZip(t), "beacon version 1.3.41 (abc) built on today, enterprise edition\n")
	dest := filepath.Join(t.TempDir(), "enterprise", "beacon.exe")
	if err := InstallUser(context.Background(), windowsZipRelease, dest); err != nil {
		t.Fatalf("InstallUser: %v", err)
	}
	if got.Name != windowsZipRelease.Name || got.Checksums.KeyURL != "https://x/k" {
		t.Fatalf("verified package = %+v", *got)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "enterprise beacon" {
		t.Fatalf("dest = %q, %v", data, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Fatalf("left behind %v, want only the binary", entries)
	}
}

func TestInstallUserRefusesAZipItShouldNotUnpack(t *testing.T) {
	for name, entries := range map[string][]zipEntry{
		"none":              {{name: "beacon-hooks.exe", content: "hooks"}},
		"nested":            {{name: "bin/beacon.exe", content: "enterprise beacon"}},
		"twice":             {{name: "beacon.exe", content: "enterprise beacon"}, {name: "./beacon.exe", content: "enterprise beacon"}},
		"a link":            {{name: "beacon.exe", content: "C:/Windows/System32/cmd.exe", mode: os.ModeSymlink | 0o777}},
		"another link":      {{name: "beacon.exe", content: "enterprise beacon"}, {name: "hooks", content: "/etc", mode: os.ModeSymlink | 0o777}},
		"outside":           {{name: "beacon.exe", content: "enterprise beacon"}, {name: "../evil.exe", content: "x"}},
		"outside backslash": {{name: "beacon.exe", content: "enterprise beacon"}, {name: `..\evil.exe`, content: "x"}},
		"absolute":          {{name: "beacon.exe", content: "enterprise beacon"}, {name: "/tmp/evil.exe", content: "x"}},
		"a drive":           {{name: "beacon.exe", content: "enterprise beacon"}, {name: `C:\evil.exe`, content: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			stubTarballInstall(t, releaseZip(t, entries...), "beacon version 1.3.41 (abc) built on today, enterprise edition\n")
			dest := filepath.Join(t.TempDir(), "enterprise", "beacon.exe")
			if err := InstallUser(context.Background(), windowsZipRelease, dest); err == nil {
				t.Fatal("installed")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("dest was written (%v)", err)
			}
		})
	}
}

func TestInstallUserRefusesAZipsCommunityBeacon(t *testing.T) {
	stubTarballInstall(t, publicLayoutZip(t), "beacon version 1.3.41 (abc) built on today\n")
	dest := filepath.Join(t.TempDir(), "enterprise", "beacon.exe")
	if err := InstallUser(context.Background(), windowsZipRelease, dest); err == nil || !strings.Contains(err.Error(), "enterprise edition") {
		t.Fatalf("err = %v", err)
	}
}

// Windows lets a running executable be renamed but not replaced, so the kept beacon.exe is moved
// aside first; what is left of earlier replacements is removed.
func TestReplaceBinaryMovesTheOldOneAside(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "beacon.exe")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(dest, "old")
	write(filepath.Join(dir, ".beacon.exe.old-1"), "older")
	bin := filepath.Join(dir, "new")
	write(bin, "new")
	if err := replaceBinary(bin, dest, true); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "new" {
		t.Fatalf("dest = %q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}

	// A first install has nothing to move.
	fresh := filepath.Join(t.TempDir(), "beacon.exe")
	write(bin, "new")
	if err := replaceBinary(bin, fresh, true); err != nil {
		t.Fatalf("first install: %v", err)
	}
}
