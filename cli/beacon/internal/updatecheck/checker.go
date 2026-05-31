package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var ErrUncomparableVersion = errors.New("latest release version could not be compared")

// Result describes the outcome of an update check.
type Result struct {
	CurrentVersion            string
	LatestVersion             string
	ReleaseURL                string
	UpdateAvailable           bool
	CurrentIsDev              bool
	UnsupportedCurrentVersion bool
}

// Checker checks whether a newer Beacon release is available.
type Checker struct {
	CurrentVersion string
	Source         Source
}

func DefaultChecker(currentVersion string) *Checker {
	return &Checker{
		CurrentVersion: currentVersion,
		Source: GitHubSource{
			Client: &http.Client{Timeout: 1500 * time.Millisecond},
		},
	}
}

func (c *Checker) Check(ctx context.Context) (Result, error) {
	current := c.CurrentVersion
	result := Result{CurrentVersion: displayVersion(current)}
	if strings.TrimSpace(current) == "dev" {
		result.CurrentIsDev = true
		return result, nil
	}
	if !CanCheckVersion(current) {
		result.UnsupportedCurrentVersion = true
		return result, nil
	}

	source := c.Source
	if source == nil {
		source = GitHubSource{}
	}
	release, err := source.Latest(ctx)
	if err != nil {
		return result, err
	}
	result.LatestVersion = displayVersion(release.Version)
	result.ReleaseURL = release.URL

	cmp, ok := compareVersions(current, release.Version)
	if !ok {
		return result, fmt.Errorf("%w: %q", ErrUncomparableVersion, release.Version)
	}
	result.UpdateAvailable = cmp < 0
	return result, nil
}
