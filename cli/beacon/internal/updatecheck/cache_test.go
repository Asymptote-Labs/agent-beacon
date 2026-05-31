package updatecheck

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheLoadFresh(t *testing.T) {
	now := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	cache := &Cache{
		Path: filepath.Join(t.TempDir(), "update-check.json"),
		TTL:  24 * time.Hour,
		Now:  func() time.Time { return now },
	}
	release := Release{Version: "v0.0.12", URL: "https://example.test/release"}
	if err := cache.Store(release); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}

	got, ok, err := cache.LoadFresh()
	if err != nil {
		t.Fatalf("LoadFresh returned error: %v", err)
	}
	if !ok {
		t.Fatal("LoadFresh ok = false, want true")
	}
	if got != release {
		t.Fatalf("LoadFresh = %#v, want %#v", got, release)
	}
}

func TestCacheLoadFreshReturnsFalseForStaleEntry(t *testing.T) {
	base := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	cache := &Cache{
		Path: filepath.Join(t.TempDir(), "update-check.json"),
		TTL:  time.Hour,
		Now:  func() time.Time { return base },
	}
	if err := cache.Store(Release{Version: "v0.0.12"}); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}
	cache.Now = func() time.Time { return base.Add(2 * time.Hour) }

	if _, ok, err := cache.LoadFresh(); err != nil {
		t.Fatalf("LoadFresh returned error: %v", err)
	} else if ok {
		t.Fatal("LoadFresh ok = true, want false for stale entry")
	}
}

func TestCacheLoadFreshReturnsErrorForCorruptEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	cache := &Cache{Path: path, TTL: time.Hour}

	if _, _, err := cache.LoadFresh(); err == nil {
		t.Fatal("LoadFresh error = nil, want corrupt cache error")
	}
}
