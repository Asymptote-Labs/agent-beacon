// Package edition installs Beacon's enterprise edition on a machine that is connected to its
// organization. The platform the machine enrolled with decides, per organization, whether it may
// have it: the root CLI proves the device with its managed-ingest device key (read into memory for
// one request, never copied), and the platform answers with the package's version, SHA-256 and a
// short-lived download link. The package is then installed with the self-updater's steps.
//
// The platform can stop an install but never choose what gets installed: the package must match
// the checksum, carry a Developer ID signature from the Apple team pinned here, pass Gatekeeper,
// and report the expected version and edition once installed, or the install is rolled back.
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
