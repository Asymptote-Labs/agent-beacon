package updatecheck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheFile is the name of the update-check cache under the user's Beacon directory.
const CacheFile = "update-check.json"

// CacheTTL is how long a cached release lookup is trusted before a refresh is due.
// Like Codex's version.json, the notice reads the cache and never waits on the network;
// a stale cache only schedules a background refresh.
const CacheTTL = 24 * time.Hour

// DisableEnv turns the automatic check off when set to 0, false, off or no.
// The explicit `beacon version check` still works.
const DisableEnv = "BEACON_UPDATE_CHECK"

// Cache is the last release lookup, persisted so the startup notice needs no network call.
// LastCheckedAt records every attempt, including failed ones, so an unreachable or
// rate-limited release API is retried once per TTL rather than on every run.
type Cache struct {
	LatestVersion string    `json:"latest_version,omitempty"`
	ReleaseURL    string    `json:"release_url,omitempty"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}

// Disabled reports whether the automatic update check is turned off by the environment.
// CI runs never check: nobody is there to read the notice.
func Disabled(getenv func(string) string) bool {
	if !PublicReleases {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv(DisableEnv))) {
	case "0", "false", "off", "no":
		return true
	}
	return strings.TrimSpace(getenv("CI")) != ""
}

// LoadCache reads the cache at path. A missing or unreadable cache is an empty one.
func LoadCache(path string) Cache {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cache{}
	}
	var cache Cache
	if err := json.Unmarshal(data, &cache); err != nil {
		return Cache{}
	}
	return cache
}

// SaveCache writes the cache atomically, 0600 under a 0700 directory.
func SaveCache(path string, cache Cache) error {
	if path == "" {
		return errors.New("update cache path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-check-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Stale reports whether a refresh is due at now.
func (c Cache) Stale(now time.Time) bool {
	if c.LastCheckedAt.IsZero() || c.LastCheckedAt.After(now) {
		return true
	}
	return now.Sub(c.LastCheckedAt) >= CacheTTL
}

// Notice returns the cached result for currentVersion. UpdateAvailable is set only when the
// cached release is strictly newer than a comparable current version, so dev and local builds
// never see a notice.
func (c Cache) Notice(currentVersion string) Result {
	result := Result{
		CurrentVersion: displayVersion(currentVersion),
		LatestVersion:  displayVersion(c.LatestVersion),
		ReleaseURL:     c.ReleaseURL,
	}
	if !CanCheckVersion(currentVersion) || c.LatestVersion == "" {
		return result
	}
	cmp, ok := compareVersions(currentVersion, c.LatestVersion)
	result.UpdateAvailable = ok && cmp < 0
	return result
}

// Record folds a check into the cache: a successful lookup replaces the release, a failed one
// only moves LastCheckedAt so the previous release is still offered.
func (c Cache) Record(result Result, err error, now time.Time) Cache {
	c.LastCheckedAt = now.UTC()
	if err == nil && result.LatestVersion != "" {
		c.LatestVersion = result.LatestVersion
		c.ReleaseURL = result.ReleaseURL
	}
	return c
}
