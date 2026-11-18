// Package edition installs Beacon's enterprise edition on a machine that is connected to its
// organization. The platform the machine enrolled with decides, per organization, whether it may
// have it: the root CLI proves the device with its managed-ingest device key (read into memory for
// one request, never copied), and the platform answers with the package's version, SHA-256 and a
// short-lived download link. The package is then installed with the self-updater's steps.
//
// The platform can stop an install but never choose what gets installed: the package must match
// the checksum, carry a Developer ID signature from the Apple team pinned here, pass Gatekeeper,
// and report the expected version and edition once installed, or the install is rolled back.
//
// A user-mode install takes only the package's beacon binary instead (InstallUser), checked the
// same way.
package edition

import (
	"bytes"
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
	"path/filepath"
	"regexp"
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

// Release is the platform's answer: one signed package and a link to it.
type Release struct {
	Version string `json:"version"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
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

// Locate asks the platform for the enterprise package: the latest release when version is empty,
// otherwise exactly that version. client may be nil.
func Locate(ctx context.Context, client *http.Client, dashboardURL, deviceKey, version string) (Release, error) {
	endpoint, err := credentialURL(dashboardURL, DownloadPath)
	if err != nil {
		return Release{}, err
	}
	body := []byte("{}")
	if version != "" {
		body, _ = json.Marshal(map[string]string{"version": strings.TrimPrefix(version, "v")})
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
	return rel, nil
}

// Install verifies and installs rel with the self-updater's steps, rolling back if the result
// does not report rel's version and the enterprise edition. currentVersion is the running CLI's,
// for the update record; logPath is where that record goes (empty: the system log).
func Install(ctx context.Context, rel Release, currentVersion, logPath string) (selfupdate.ApplyResult, error) {
	a := selfupdate.NewApplier(currentVersion)
	a.LogPath = logPath
	return a.InstallPackage(ctx, selfupdate.Package{
		URL:     rel.URL,
		SHA256:  rel.SHA256,
		Version: rel.Version,
		Edition: Enterprise,
	})
}

// Seams, so tests reach neither the network nor pkgutil.
var (
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
// The package is checked as Install checks it (checksum, Beacon's pinned signing team,
// Gatekeeper); its signature covers the payload, so the binary is then taken out of it with
// pkgutil, and must report rel's version and the enterprise edition before it replaces dest.
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

	pkgPath := filepath.Join(stage, "enterprise.pkg")
	pkg := selfupdate.Package{URL: rel.URL, SHA256: rel.SHA256, Version: rel.Version, Edition: Enterprise}
	if err := fetchVerifiedPackage(ctx, pkg, pkgPath); err != nil {
		return err
	}
	expanded := filepath.Join(stage, "expanded")
	if out, err := runCommand(ctx, "pkgutil", "--expand-full", pkgPath, expanded); err != nil {
		return fmt.Errorf("pkgutil --expand-full: %s: %w", strings.TrimSpace(out), err)
	}
	// A component package (pkgbuild) expands to Payload/; a product archive to <component>/Payload/.
	var found []string
	for _, pattern := range []string{"Payload", filepath.Join("*", "Payload")} {
		matches, _ := filepath.Glob(filepath.Join(expanded, pattern, "opt", "beacon", "bin", "beacon"))
		found = append(found, matches...)
	}
	if len(found) != 1 {
		return fmt.Errorf("the package holds %d copies of opt/beacon/bin/beacon, want 1", len(found))
	}
	bin := found[0]
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
