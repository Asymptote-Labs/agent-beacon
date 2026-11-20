// Package edition installs Beacon's enterprise edition on a machine that is connected to its
// organization. The platform the machine enrolled with decides, per organization, whether it may
// have it: the root CLI proves the device with its managed-ingest device key (read into memory for
// one request, never copied) and names the package it can install (Target), and the platform
// answers with the package's version, SHA-256 and a short-lived download link. The package is then
// installed with the self-updater's steps.
//
// The platform can stop an install but never choose what gets installed: the package must match
// the checksum, and report the expected version and edition once installed, or the install is
// rolled back. On macOS it must also carry a Developer ID signature from the Apple team pinned in
// selfupdate and pass Gatekeeper; on Linux and Windows it must be listed in a checksums.txt signed
// by Beacon's pinned release key, whose links the platform's answer carries beside the package's.
//
// A user-mode install takes only the package's beacon binary instead (InstallUser), checked the
// same way: from the macOS package, on Linux from the release's tarball, and on Windows from the
// release's zip.
package edition

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

const (
	// Enterprise is the edition this package installs.
	Enterprise = "enterprise"
	// DownloadPath is the platform route that answers with a package for a device key.
	DownloadPath = "/api/cli/enforcement/download"

	requestTimeout = 30 * time.Second
	maxAnswer      = 64 << 10
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Package formats a machine can ask for.
const (
	FormatPkg     = "pkg"
	FormatDeb     = "deb"
	FormatRPM     = "rpm"
	FormatTarball = "tar.gz"
	FormatMSI     = "msi"
	FormatZip     = "zip"
)

// Target is the package a machine asks for: its OS and architecture, as Go names them, and the
// format it installs.
type Target struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format"`
}

// SystemTarget is the package a system install takes: the macOS package, on Linux the native
// package of whichever of dpkg and rpm this host has, and on Windows the MSI.
func SystemTarget() Target { return systemTarget(runtime.GOOS, runtime.GOARCH) }

func systemTarget(goos, goarch string) Target {
	t := Target{OS: goos, Arch: goarch, Format: FormatPkg}
	switch goos {
	case "linux":
		t.Format = FormatDeb
		if _, err := lookPath("dpkg"); err != nil {
			if _, err := lookPath("rpm"); err == nil {
				t.Format = FormatRPM
			}
		}
	case "windows":
		t.Format = FormatMSI
	}
	return t
}

// UserTarget is the package a user-mode install takes its beacon from: the macOS package, which
// InstallUser expands without installing, on Linux the release's tarball, since a user cannot
// install a .deb or .rpm, and on Windows the release's zip, since an MSI installs for the machine.
func UserTarget() Target { return userTarget(runtime.GOOS, runtime.GOARCH) }

func userTarget(goos, goarch string) Target {
	t := Target{OS: goos, Arch: goarch, Format: FormatPkg}
	switch goos {
	case "linux":
		t.Format = FormatTarball
	case "windows":
		t.Format = FormatZip
	}
	return t
}

// verifiedBySignedChecksums reports whether target's packages are held to the release's signed
// checksums.txt, whose links the platform's answer must then carry: everywhere but macOS, whose
// packages carry Beacon's Developer ID signature instead.
func verifiedBySignedChecksums(target Target) bool {
	return target.OS == "linux" || target.OS == "windows"
}

// rollsBack reports whether a failed install of target's package is rolled back by reinstalling the
// public package of the version installed now (Release.Rollback): a Linux .deb or .rpm, whose
// package database would otherwise name the enterprise package, and a Windows MSI, whose files
// belong to Windows Installer and are put back only by installing the previous MSI.
func rollsBack(target Target) bool {
	switch target.OS {
	case "linux":
		return target.Format == FormatDeb || target.Format == FormatRPM
	case "windows":
		return target.Format == FormatMSI
	}
	return false
}

// Release is the platform's answer: one signed package and a link to it, and on Linux links to the
// release's signed checksums.txt.
type Release struct {
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Checksums Checksums `json:"checksums"`
	// Rollback is set on a Linux system install over the public edition: see Rollback.
	Rollback *Rollback `json:"rollback,omitempty"`
}

// Rollback is the public package of the version installed now, which a failed install on Linux
// reinstalls through the package manager, with links to its own release's signed checksums.txt.
// The platform returns it when asked (rollback_from), so it comes from wherever the enterprise
// package does rather than from GitHub directly.
type Rollback struct {
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	SHA256    string    `json:"sha256"`
	Checksums Checksums `json:"checksums"`
}

// Checksums are links to the release's checksums.txt, its signature, and the public key that made
// it, which is trusted only for carrying a fingerprint selfupdate pins.
type Checksums struct {
	URL          string `json:"url"`
	SignatureURL string `json:"signature_url"`
	KeyURL       string `json:"key_url"`
}

func (r Release) pkg() selfupdate.Package {
	return selfupdate.Package{
		URL:     r.URL,
		SHA256:  r.SHA256,
		Version: r.Version,
		Edition: Enterprise,
		Name:    r.Name,
		Checksums: selfupdate.SignedChecksums{
			URL:          r.Checksums.URL,
			SignatureURL: r.Checksums.SignatureURL,
			KeyURL:       r.Checksums.KeyURL,
		},
	}
}

// HTTPError is a refusal from the platform, with its own explanation.
type HTTPError struct {
	Status int
	Detail string
}

func (e *HTTPError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Locate asks the platform for the enterprise package a system install takes (SystemTarget): the
// latest release when version is empty, otherwise exactly that version. client may be nil.
func Locate(ctx context.Context, client *http.Client, dashboardURL, deviceKey, version string) (Release, error) {
	return LocateFor(ctx, client, dashboardURL, deviceKey, version, SystemTarget())
}

// LocateFor is Locate for the package target names.
func LocateFor(ctx context.Context, client *http.Client, dashboardURL, deviceKey, version string, target Target) (Release, error) {
	endpoint, err := credentialURL(dashboardURL, DownloadPath)
	if err != nil {
		return Release{}, err
	}
	// A platform that predates os, arch and format ignores them and answers with the macOS package,
	// which checkAnswer refuses anywhere else.
	ask := struct {
		Version string `json:"version,omitempty"`
		Target
		RollbackFrom string `json:"rollback_from,omitempty"`
	}{Version: strings.TrimPrefix(version, "v"), Target: target}
	if rollsBack(target) {
		ask.RollbackFrom = publicVersionInstalled(ctx)
	}
	body, err := json.Marshal(ask)
	if err != nil {
		return Release{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	c := *client
	// The device key is a bearer credential: it must never follow a redirect.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Authorization", "Bearer "+deviceKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return Release{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var refusal struct {
			Detail any `json:"detail"`
		}
		_ = json.Unmarshal(data, &refusal)
		detail, _ := refusal.Detail.(string)
		return Release{}, &HTTPError{Status: resp.StatusCode, Detail: detail}
	}
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return Release{}, fmt.Errorf("the platform's answer is not JSON: %w", err)
	}
	rel.SHA256 = strings.ToLower(strings.TrimSpace(rel.SHA256))
	if rel.Rollback != nil {
		rel.Rollback.SHA256 = strings.ToLower(strings.TrimSpace(rel.Rollback.SHA256))
	}
	switch {
	case !updatecheck.CanCheckVersion(rel.Version):
		return Release{}, fmt.Errorf("the platform named no release version (%q)", rel.Version)
	case version != "" && rel.Version != strings.TrimPrefix(version, "v"):
		return Release{}, fmt.Errorf("asked for %s, the platform answered with %s", version, rel.Version)
	case !sha256Hex.MatchString(rel.SHA256):
		return Release{}, errors.New("the platform's answer has no SHA-256")
	case !strings.HasPrefix(rel.URL, "https://"):
		return Release{}, errors.New("the platform's answer has no https download link")
	}
	if err := checkAnswer(rel, target, ask.RollbackFrom); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// checkAnswer refuses a release that is not the package target asked for, which a platform that
// offers only the macOS package answers with, and on Linux or Windows one without the links to its
// signed checksums.txt, or, when rollbackFrom asked for one, without the public package of that
// version.
func checkAnswer(rel Release, target Target, rollbackFrom string) error {
	if !verifiedBySignedChecksums(target) {
		return nil
	}
	if strings.ContainsAny(rel.Name, `/\`) || !strings.HasSuffix(rel.Name, "."+target.Format) {
		return fmt.Errorf("the platform answered with %q, not a %s package for %s/%s; it may not offer the enterprise edition for %s yet",
			rel.Name, target.Format, target.OS, target.Arch, osName(target.OS))
	}
	if !httpsLinks(rel.URL, rel.Checksums) {
		return errors.New("the platform's answer has no https links to the release's signed checksums")
	}
	if rollbackFrom == "" {
		return nil
	}
	back := rel.Rollback
	want := publicPackageName(rollbackFrom, target)
	switch {
	case back == nil:
		return fmt.Errorf("the platform's answer has no public %s package of %s, the version installed here, which a failed install would reinstall", target.Format, rollbackFrom)
	case back.Version != rollbackFrom || back.Name != want:
		return fmt.Errorf("asked for %s to roll back to, the platform answered with %q", want, back.Name)
	case back.SHA256 != "" && !sha256Hex.MatchString(back.SHA256):
		return fmt.Errorf("the platform's checksum of %s is not a SHA-256", want)
	case !httpsLinks(back.URL, back.Checksums):
		return fmt.Errorf("the platform's answer has no https links to %s and its release's signed checksums", want)
	}
	return nil
}

// httpsLinks reports whether a package's link and its signed checksums' links are all https.
func httpsLinks(url string, c Checksums) bool {
	for _, link := range []string{url, c.URL, c.SignatureURL, c.KeyURL} {
		if !strings.HasPrefix(link, "https://") {
			return false
		}
	}
	return true
}

// publicPackageName is the public release's name for its package of version for target: the
// GoReleaser package on Linux, and on Windows the MSI release.yml builds, named for x64.
func publicPackageName(version string, target Target) string {
	if target.OS == "windows" {
		arch := target.Arch
		if arch == "amd64" {
			arch = "x64"
		}
		return fmt.Sprintf("BeaconEndpointAgent-%s-%s.msi", version, arch)
	}
	return fmt.Sprintf("beacon_%s_linux_%s.%s", version, target.Arch, target.Format)
}

func osName(goos string) string {
	switch goos {
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	}
	return goos
}

// publicVersionInstalled is the version of the public edition installed here, or "" when what is
// installed is the enterprise edition (or no release at all, which Install refuses).
func publicVersionInstalled(ctx context.Context) string {
	line := installedVersionLine(ctx)
	version := selfupdate.VersionLineVersion(line)
	if selfupdate.VersionLineEdition(line) == Enterprise || !updatecheck.CanCheckVersion(version) {
		return ""
	}
	return version
}

// Install verifies and installs rel with the self-updater's steps, rolling back if the result
// does not report rel's version and the enterprise edition. currentVersion is the running CLI's,
// for the update record; logPath is where that record goes (empty: the system log).
func Install(ctx context.Context, rel Release, currentVersion, logPath string) (selfupdate.ApplyResult, error) {
	a := selfupdate.NewApplier(currentVersion)
	a.LogPath = logPath
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		back, err := rollbackPackage(ctx, rel)
		if err != nil {
			return selfupdate.ApplyResult{}, err
		}
		a.RollbackPackage = back
	}
	return a.InstallPackage(ctx, rel.pkg())
}

// rollbackPackage is the package a failed install of rel reinstalls on Linux or Windows: the public
// package of the version installed now, from the platform's answer (Release.Rollback). On Linux the
// enterprise package's epoch ranks it above every public release, so a rollback that only copied
// the public files back would leave the package database naming the enterprise package, and
// neither apt, dnf nor the next public update would ever replace it. On Windows the files belong to
// Windows Installer, and reinstalling the previous MSI is the only rollback there is. Over an
// enterprise install it is nil: on Linux the database then names a newer enterprise package than
// the files, which the next enterprise update still replaces, and on Windows the self-updater
// reports that it had nothing to reinstall.
func rollbackPackage(ctx context.Context, rel Release) (*selfupdate.Package, error) {
	line := installedVersionLine(ctx)
	if selfupdate.VersionLineEdition(line) == Enterprise {
		return nil, nil
	}
	version := selfupdate.VersionLineVersion(line)
	if !updatecheck.CanCheckVersion(version) {
		return nil, fmt.Errorf("the installed beacon (%q) is not a public release, so a failed install could not be rolled back by reinstalling its package", strings.TrimSpace(line))
	}
	back := rel.Rollback
	if back == nil || back.Version != version {
		return nil, fmt.Errorf("the platform named no public package of %s, the version installed here, for a failed install to reinstall", version)
	}
	return &selfupdate.Package{
		URL:     back.URL,
		SHA256:  strings.ToLower(back.SHA256),
		Version: back.Version,
		Name:    back.Name,
		Checksums: selfupdate.SignedChecksums{
			URL:          back.Checksums.URL,
			SignatureURL: back.Checksums.SignatureURL,
			KeyURL:       back.Checksums.KeyURL,
		},
	}, nil
}

// Seams, so tests reach neither the network, pkgutil nor this host's package tools.
var (
	lookPath             = exec.LookPath
	installedVersionLine = func(ctx context.Context) string {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, selfupdate.SystemBeaconPath(), "version").Output()
		return string(out)
	}
	fetchVerifiedPackage = func(ctx context.Context, pkg selfupdate.Package, dest string) error {
		return selfupdate.NewApplier("").FetchVerifiedPackage(ctx, pkg, dest)
	}
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		return string(out), err
	}
)

// InstallUser puts rel's beacon at dest, for a user-mode install, which has no root to run the
// package's installer with and must not let its postinstall set the machine up in system mode.
// The package is checked as Install checks it (checksum, and Beacon's pinned signing team and
// Gatekeeper on macOS, Beacon's signed checksums.txt on Linux and Windows); the binary is then taken out of
// it, and must report rel's version and the enterprise edition before it replaces dest.
func InstallUser(ctx context.Context, rel Release, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	tarball := strings.HasSuffix(rel.Name, "."+FormatTarball)
	zipped := strings.HasSuffix(rel.Name, "."+FormatZip)
	pkgPath := filepath.Join(stage, "enterprise.pkg")
	switch {
	case tarball:
		pkgPath = filepath.Join(stage, "enterprise.tar.gz")
	case zipped:
		pkgPath = filepath.Join(stage, "enterprise.zip")
	}
	if err := fetchVerifiedPackage(ctx, rel.pkg(), pkgPath); err != nil {
		return err
	}
	var bin string
	switch {
	case tarball:
		bin = filepath.Join(stage, "beacon")
		err = extractBeacon(pkgPath, bin)
	case zipped:
		bin = filepath.Join(stage, "beacon.exe")
		err = extractZipBeacon(pkgPath, bin)
	default:
		bin, err = expandPackageBeacon(ctx, pkgPath, filepath.Join(stage, "expanded"))
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	out, err := runCommand(ctx, bin, "version")
	if err != nil {
		return fmt.Errorf("the package's beacon did not run: %s: %w", strings.TrimSpace(out), err)
	}
	if !selfupdate.VersionLineMatches(out, rel.Version) || selfupdate.VersionLineEdition(out) != Enterprise {
		line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return fmt.Errorf("the package's beacon reports %q, want %s, %s edition", line, rel.Version, Enterprise)
	}
	return replaceBinary(bin, dest, moveRunningAside)
}

// moveRunningAside is whether replaceBinary moves the binary it replaces aside first, as Windows
// needs: it does not let a running executable be replaced or removed, only renamed, and the kept
// enterprise beacon.exe may be running (an agent's hook install it was handed, or its own enable).
var moveRunningAside = runtime.GOOS == "windows"

// replaceBinary puts bin at dest. With aside, a binary already at dest is first renamed to a hidden
// file beside it (the same volume, so the rename cannot turn into a copy) and removed once replaced,
// which fails while it still runs; the next install removes what is left. A failed replacement puts
// it back.
func replaceBinary(bin, dest string, aside bool) error {
	if !aside {
		return os.Rename(bin, dest)
	}
	dir, base := filepath.Dir(dest), filepath.Base(dest)
	prefix := "." + base + ".old-"
	if earlier, err := filepath.Glob(filepath.Join(dir, prefix+"*")); err == nil {
		for _, path := range earlier {
			_ = os.Remove(path)
		}
	}
	if _, err := os.Lstat(dest); os.IsNotExist(err) {
		return os.Rename(bin, dest)
	} else if err != nil {
		return err
	}
	moved := filepath.Join(dir, fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()))
	if err := os.Rename(dest, moved); err != nil {
		return fmt.Errorf("moving the kept %s aside to replace it: %w", base, err)
	}
	if err := os.Rename(bin, dest); err != nil {
		_ = os.Rename(moved, dest)
		return err
	}
	_ = os.Remove(moved)
	return nil
}

// expandPackageBeacon expands the macOS package at pkgPath into expanded with pkgutil, whose
// signature covers the payload, and returns the path of the one opt/beacon/bin/beacon in it.
func expandPackageBeacon(ctx context.Context, pkgPath, expanded string) (string, error) {
	if out, err := runCommand(ctx, "pkgutil", "--expand-full", pkgPath, expanded); err != nil {
		return "", fmt.Errorf("pkgutil --expand-full: %s: %w", strings.TrimSpace(out), err)
	}
	// A component package (pkgbuild) expands to Payload/; a product archive to <component>/Payload/.
	var found []string
	for _, pattern := range []string{"Payload", filepath.Join("*", "Payload")} {
		matches, _ := filepath.Glob(filepath.Join(expanded, pattern, "opt", "beacon", "bin", "beacon"))
		found = append(found, matches...)
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the package holds %d copies of opt/beacon/bin/beacon, want 1", len(found))
	}
	return found[0], nil
}

// maxBeaconBytes bounds the binary extractBeacon writes.
const maxBeaconBytes = 512 << 20

// extractBeacon writes the beacon at the root of the release tarball at tarball to dest. The
// tarball is laid out as the public release's archives are, with the binaries at its root; nothing
// else in it is written.
func extractBeacon(tarball, dest string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("the release tarball: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the release tarball: %w", err)
		}
		if path.Clean(strings.TrimPrefix(hdr.Name, "./")) != "beacon" {
			continue
		}
		if found {
			return errors.New("the release tarball holds more than one beacon")
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size > maxBeaconBytes {
			return errors.New("the release tarball's beacon is not a regular file of a sensible size")
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxBeaconBytes))
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("the release tarball holds no beacon at its root")
	}
	return nil
}

// extractZipBeacon writes the beacon.exe at the root of the Windows release zip at archive to dest.
// The zip is laid out as the public release's Windows archive is, with beacon.exe, beacon-hooks.exe
// and beacon-otelcol.exe at its root; only beacon.exe is written, as only beacon is from the Linux
// tarball, since it embeds the hooks binary it installs. The archive is refused whole if any entry
// names an absolute path, climbs out with "..", or is a link, rather than skipped past: a release
// archive carries none of them.
func extractZipBeacon(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("the release zip: %w", err)
	}
	defer zr.Close()
	var found *zip.File
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasPrefix(name, "/") || filepath.IsAbs(f.Name) || filepath.VolumeName(f.Name) != "" || strings.Contains(name, ":") {
			return fmt.Errorf("the release zip names an absolute path (%q)", f.Name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return fmt.Errorf("the release zip names a path outside itself (%q)", f.Name)
			}
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || (mode.Type() != 0 && !mode.IsDir()) {
			return fmt.Errorf("the release zip holds a link or special file (%q)", f.Name)
		}
		if path.Clean(strings.TrimPrefix(name, "./")) != "beacon.exe" {
			continue
		}
		if found != nil {
			return errors.New("the release zip holds more than one beacon.exe")
		}
		if !mode.IsRegular() || f.UncompressedSize64 > maxBeaconBytes {
			return errors.New("the release zip's beacon.exe is not a regular file of a sensible size")
		}
		found = f
	}
	if found == nil {
		return errors.New("the release zip holds no beacon.exe at its root")
	}
	in, err := found.Open()
	if err != nil {
		return fmt.Errorf("the release zip: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, maxBeaconBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n > maxBeaconBytes {
		err = errors.New("the release zip's beacon.exe is larger than it says")
	}
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("the release zip: %w", err)
	}
	return nil
}

// credentialURL joins base and path, and requires https (plain http only to a loopback host,
// for tests), because the request carries the device key.
func credentialURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid platform URL %q", base)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
		return "", fmt.Errorf("refusing to send the device key to %s: https is required", u.Redacted())
	}
	return u.JoinPath(path).String(), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
