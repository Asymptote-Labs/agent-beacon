package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var ErrUncomparableVersion = errors.New("latest release version could not be compared")

// Result describes the outcome of an update check.
type Result struct {
	CurrentVersion  string
	LatestVersion   string
	ReleaseURL      string
	UpdateAvailable bool
	CurrentIsDev    bool
	FromCache       bool
}

// Checker checks whether a newer Beacon release is available.
type Checker struct {
	CurrentVersion string
	Source         Source
	Cache          *Cache
}

func DefaultChecker(currentVersion string) *Checker {
	cache, _ := DefaultCache()
	return &Checker{
		CurrentVersion: currentVersion,
		Source: GitHubSource{
			Client: &http.Client{Timeout: 1500 * time.Millisecond},
		},
		Cache: cache,
	}
}

func (c *Checker) Check(ctx context.Context) (Result, error) {
	current := c.CurrentVersion
	result := Result{CurrentVersion: displayVersion(current)}
	if !CanCheckVersion(current) {
		result.CurrentIsDev = true
		return result, nil
	}

	release, fromCache, err := c.latest(ctx)
	if err != nil {
		return result, err
	}
	result.LatestVersion = displayVersion(release.Version)
	result.ReleaseURL = release.URL
	result.FromCache = fromCache

	cmp, ok := compareVersions(current, release.Version)
	if !ok {
		return result, fmt.Errorf("%w: %q", ErrUncomparableVersion, release.Version)
	}
	result.UpdateAvailable = cmp < 0
	return result, nil
}

func (c *Checker) latest(ctx context.Context) (Release, bool, error) {
	if c.Cache != nil {
		if release, ok, err := c.Cache.LoadFresh(); err == nil && ok {
			return release, true, nil
		}
	}
	source := c.Source
	if source == nil {
		source = GitHubSource{}
	}
	release, err := source.Latest(ctx)
	if err != nil {
		return Release{}, false, err
	}
	if c.Cache != nil {
		_ = c.Cache.Store(release)
	}
	return release, false, nil
}
