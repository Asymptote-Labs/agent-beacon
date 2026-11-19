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
// selfupdate and pass Gatekeeper; on Linux it must be listed in a checksums.txt signed by Beacon's
// pinned release key, whose links the platform's answer carries beside the package's.
//
// A user-mode install takes only the package's beacon binary instead (InstallUser), checked the
// same way: from the macOS package, or on Linux from the release's tarball.
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
)

// Target is the package a machine asks for: its OS and architecture, as Go names them, and the
// format it installs.
type Target struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format"`
}

// SystemTarget is the package a system install takes: the macOS package, or on Linux the native
// package of whichever of dpkg and rpm this host has.
func SystemTarget() Target {
	t := Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Format: FormatPkg}
	if t.OS == "linux" {
		t.Format = FormatDeb
		if _, err := lookPath("dpkg"); err != nil {
			if _, err := lookPath("rpm"); err == nil {
				t.Format = FormatRPM
			}
		}
	}
	return t
}

// UserTarget is the package a user-mode install takes its beacon from: the macOS package, which
// InstallUser expands without installing, or on Linux the release's tarball, since a user cannot
// install a .deb or .rpm.
func UserTarget() Target {
	t := Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Format: FormatPkg}
	if t.OS == "linux" {
		t.Format = FormatTarball
	}
	return t
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
	}{Version: strings.TrimPrefix(version, "v"), Target: target}
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
	if err := checkAnswer(rel, target); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// checkAnswer refuses a release that is not the package target asked for, which a platform that
// offers only the macOS package answers with, and on Linux one without the links to its signed
// checksums.txt.
func checkAnswer(rel Release, target Target) error {
	if target.OS != "linux" {
		return nil
	}
	if strings.ContainsAny(rel.Name, `/\`) || !strings.HasSuffix(rel.Name, "."+target.Format) {
		return fmt.Errorf("the platform answered with %q, not a %s package for linux/%s; it may not offer the enterprise edition for Linux yet", rel.Name, target.Format, target.Arch)
	}
	for _, link := range []string{rel.Checksums.URL, rel.Checksums.SignatureURL, rel.Checksums.KeyURL} {
		if !strings.HasPrefix(link, "https://") {
			return errors.New("the platform's answer has no https links to the release's signed checksums")
		}
	}
	return nil
}

// Install verifies and installs rel with the self-updater's steps, rolling back if the result
// does not report rel's version and the enterprise edition. currentVersion is the running CLI's,
// for the update record; logPath is where that record goes (empty: the system log).
func Install(ctx context.Context, rel Release, currentVersion, logPath string) (selfupdate.ApplyResult, error) {
	a := selfupdate.NewApplier(currentVersion)
	a.LogPath = logPath
	return a.InstallPackage(ctx, rel.pkg())
}

// Seams, so tests reach neither the network, pkgutil nor this host's package tools.
var (
	lookPath             = exec.LookPath
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
// Gatekeeper on macOS, Beacon's signed checksums.txt on Linux); the binary is then taken out of
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
	pkgPath := filepath.Join(stage, "enterprise.pkg")
	if tarball {
		pkgPath = filepath.Join(stage, "enterprise.tar.gz")
	}
	if err := fetchVerifiedPackage(ctx, rel.pkg(), pkgPath); err != nil {
		return err
	}
	var bin string
	if tarball {
		bin = filepath.Join(stage, "beacon")
		err = extractBeacon(pkgPath, bin)
	} else {
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
	return os.Rename(bin, dest)
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
