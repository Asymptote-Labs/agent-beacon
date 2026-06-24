package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
)

// ManifestURLEnv overrides the update manifest location. This is the seam a
// managed/enterprise update path uses to point Beacon at an internal host
// without a code change.
const ManifestURLEnv = "BEACON_UPDATE_MANIFEST_URL"

// DefaultManifestURL resolves to the manifest published with the most recent
// GitHub release. GitHub's `releases/latest/download/<asset>` path redirects to
// the latest release's asset, so the updater does not need the GitHub API here.
const DefaultManifestURL = "https://github.com/asymptote-labs/agent-beacon/releases/latest/download/update-manifest.json"

// CurrentManifestSchema is the manifest schema version this build understands.
const CurrentManifestSchema = 1

// Artifact is a single downloadable update package and its expected digest.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// UpdateManifest is the small JSON document published as a release asset that
// tells an installed agent what the latest version is and where to fetch it.
type UpdateManifest struct {
	Schema              int                 `json:"schema"`
	Version             string              `json:"version"`
	MinSupportedVersion string              `json:"min_supported_version,omitempty"`
	TeamID              string              `json:"team_id,omitempty"`
	PkgIdentifier       string              `json:"pkg_identifier,omitempty"`
	Artifacts           map[string]Artifact `json:"artifacts"`
}

// ManifestURL returns the configured manifest URL, honoring the env override.
func ManifestURL() string {
	if v := strings.TrimSpace(os.Getenv(ManifestURLEnv)); v != "" {
		return v
	}
	return DefaultManifestURL
}

// RuntimeArchKey is the artifacts map key for the running platform, e.g.
// "darwin_arm64".
func RuntimeArchKey() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}

// ParseManifest decodes and validates a manifest document.
func ParseManifest(data []byte) (UpdateManifest, error) {
	var m UpdateManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return UpdateManifest{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if m.Schema == 0 {
		return UpdateManifest{}, fmt.Errorf("update manifest missing schema")
	}
	if m.Schema > CurrentManifestSchema {
		return UpdateManifest{}, fmt.Errorf("update manifest schema %d is newer than supported %d", m.Schema, CurrentManifestSchema)
	}
	if strings.TrimSpace(m.Version) == "" {
		return UpdateManifest{}, fmt.Errorf("update manifest missing version")
	}
	if _, ok := parseReleaseVersion(m.Version); !ok {
		return UpdateManifest{}, fmt.Errorf("update manifest version %q is not a release version", m.Version)
	}
	return m, nil
}

// ArtifactFor returns the artifact for the given arch key, if present.
func (m UpdateManifest) ArtifactFor(archKey string) (Artifact, bool) {
	a, ok := m.Artifacts[archKey]
	if !ok || strings.TrimSpace(a.URL) == "" || strings.TrimSpace(a.SHA256) == "" {
		return Artifact{}, false
	}
	return a, true
}

// ManifestSource fetches the update manifest over HTTP.
type ManifestSource struct {
	Client   *http.Client
	Endpoint string
}

// Fetch retrieves and parses the manifest.
func (s ManifestSource) Fetch(ctx context.Context) (UpdateManifest, error) {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = ManifestURL()
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return UpdateManifest{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beacon-self-update")

	resp, err := client.Do(req)
	if err != nil {
		return UpdateManifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UpdateManifest{}, fmt.Errorf("update manifest lookup returned HTTP %d", resp.StatusCode)
	}

	// Cap the body; the manifest is tiny.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return UpdateManifest{}, err
	}
	return ParseManifest(data)
}

// ManifestResult describes the outcome of comparing the running build against a
// fetched manifest.
type ManifestResult struct {
	CurrentVersion            string
	LatestVersion             string
	UpdateAvailable           bool
	CurrentIsDev              bool
	UnsupportedCurrentVersion bool
	BelowMinSupported         bool
	Manifest                  UpdateManifest
}

// EvaluateManifest compares the current build version to a manifest. It mirrors
// Checker.Check's dev/unsupported handling so callers behave consistently.
func EvaluateManifest(current string, m UpdateManifest) (ManifestResult, error) {
	result := ManifestResult{
		CurrentVersion: displayVersion(current),
		LatestVersion:  displayVersion(m.Version),
		Manifest:       m,
	}
	if strings.TrimSpace(current) == "dev" {
		result.CurrentIsDev = true
		return result, nil
	}
	if !CanCheckVersion(current) {
		result.UnsupportedCurrentVersion = true
		return result, nil
	}

	cmp, ok := compareVersions(current, m.Version)
	if !ok {
		return result, fmt.Errorf("%w: %q", ErrUncomparableVersion, m.Version)
	}
	result.UpdateAvailable = cmp < 0

	if m.MinSupportedVersion != "" {
		if minCmp, ok := compareVersions(current, m.MinSupportedVersion); ok && minCmp < 0 {
			result.BelowMinSupported = true
		}
	}
	return result, nil
}
