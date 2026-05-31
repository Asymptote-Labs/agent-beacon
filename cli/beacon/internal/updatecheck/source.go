package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const DefaultLatestReleaseURL = "https://api.github.com/repos/asymptote-labs/agent-beacon/releases/latest"

// Release is the latest Beacon release known to an update source.
type Release struct {
	Version string
	URL     string
}

// Source returns the latest available Beacon release.
type Source interface {
	Latest(context.Context) (Release, error)
}

// GitHubSource reads release metadata from GitHub's latest-release endpoint.
type GitHubSource struct {
	Client   *http.Client
	Endpoint string
}

func (s GitHubSource) Latest(ctx context.Context) (Release, error) {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = DefaultLatestReleaseURL
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "beacon-update-check")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release lookup returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Release{}, err
	}
	if payload.TagName == "" {
		return Release{}, fmt.Errorf("release lookup did not include tag_name")
	}
	return Release{Version: payload.TagName, URL: payload.HTMLURL}, nil
}
