package updatecheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const DefaultCacheTTL = 24 * time.Hour

// Cache stores the most recently observed latest release.
type Cache struct {
	Path string
	TTL  time.Duration
	Now  func() time.Time
}

type cacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
	ReleaseURL    string    `json:"release_url"`
}

func DefaultCache() (*Cache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Cache{
		Path: filepath.Join(base, "beacon", "update-check.json"),
		TTL:  DefaultCacheTTL,
		Now:  time.Now,
	}, nil
}

func (c *Cache) LoadFresh() (Release, bool, error) {
	if c == nil || c.Path == "" {
		return Release{}, false, nil
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Release{}, false, nil
		}
		return Release{}, false, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Release{}, false, err
	}
	if entry.LatestVersion == "" {
		return Release{}, false, nil
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if now().Sub(entry.CheckedAt) > ttl {
		return Release{}, false, nil
	}
	return Release{Version: entry.LatestVersion, URL: entry.ReleaseURL}, true, nil
}

func (c *Cache) Store(release Release) error {
	if c == nil || c.Path == "" || release.Version == "" {
		return nil
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	entry := cacheEntry{
		CheckedAt:     now().UTC(),
		LatestVersion: release.Version,
		ReleaseURL:    release.URL,
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-check-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, c.Path)
}
