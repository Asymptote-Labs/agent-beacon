package updatecheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeSource struct {
	release Release
	err     error
	calls   int
}

func (s *fakeSource) Latest(context.Context) (Release, error) {
	s.calls++
	if s.err != nil {
		return Release{}, s.err
	}
	return s.release, nil
}

func TestCheckerUsesFreshCache(t *testing.T) {
	now := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	cache := &Cache{
		Path: filepath.Join(t.TempDir(), "update-check.json"),
		TTL:  24 * time.Hour,
		Now:  func() time.Time { return now },
	}
	if err := cache.Store(Release{Version: "v0.0.12"}); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}
	source := &fakeSource{release: Release{Version: "v0.0.13"}}
	checker := &Checker{CurrentVersion: "v0.0.10", Source: source, Cache: cache}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !got.UpdateAvailable || got.LatestVersion != "v0.0.12" || !got.FromCache {
		t.Fatalf("Check = %#v, want cached available update", got)
	}
	if source.calls != 0 {
		t.Fatalf("source calls = %d, want 0", source.calls)
	}
}

func TestCheckerFetchesWhenCacheIsStale(t *testing.T) {
	base := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	cache := &Cache{
		Path: filepath.Join(t.TempDir(), "update-check.json"),
		TTL:  time.Hour,
		Now:  func() time.Time { return base },
	}
	if err := cache.Store(Release{Version: "v0.0.11"}); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}
	cache.Now = func() time.Time { return base.Add(2 * time.Hour) }
	source := &fakeSource{release: Release{Version: "v0.0.12"}}
	checker := &Checker{CurrentVersion: "v0.0.10", Source: source, Cache: cache}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !got.UpdateAvailable || got.LatestVersion != "v0.0.12" || got.FromCache {
		t.Fatalf("Check = %#v, want fetched available update", got)
	}
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
}

func TestCheckerIgnoresCorruptCacheAndFetches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	if err := os.WriteFile(path, []byte("{bad-json"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	cache := &Cache{Path: path}
	source := &fakeSource{release: Release{Version: "v0.0.10"}}
	checker := &Checker{CurrentVersion: "v0.0.10", Source: source, Cache: cache}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.UpdateAvailable {
		t.Fatalf("Check UpdateAvailable = true, want false")
	}
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
}

func TestCheckerReturnsDevResultWithoutSourceLookup(t *testing.T) {
	source := &fakeSource{release: Release{Version: "v0.0.12"}}
	checker := &Checker{CurrentVersion: "dev", Source: source}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !got.CurrentIsDev {
		t.Fatalf("CurrentIsDev = false, want true")
	}
	if source.calls != 0 {
		t.Fatalf("source calls = %d, want 0", source.calls)
	}
}

func TestCheckerReturnsUncomparableVersionError(t *testing.T) {
	checker := &Checker{
		CurrentVersion: "v0.0.10",
		Source:         &fakeSource{release: Release{Version: "latest"}},
	}

	_, err := checker.Check(context.Background())
	if !errors.Is(err, ErrUncomparableVersion) {
		t.Fatalf("Check error = %v, want ErrUncomparableVersion", err)
	}
}
