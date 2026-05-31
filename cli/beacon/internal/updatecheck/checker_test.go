package updatecheck

import (
	"context"
	"errors"
	"testing"
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

func TestCheckerFetchesLatestRelease(t *testing.T) {
	source := &fakeSource{release: Release{Version: "v0.0.12"}}
	checker := &Checker{CurrentVersion: "v0.0.10", Source: source}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !got.UpdateAvailable || got.LatestVersion != "v0.0.12" {
		t.Fatalf("Check = %#v, want available update", got)
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

func TestCheckerReturnsUnsupportedCurrentVersionWithoutSourceLookup(t *testing.T) {
	source := &fakeSource{release: Release{Version: "v0.0.12"}}
	checker := &Checker{CurrentVersion: "v0.0.12+local", Source: source}

	got, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !got.UnsupportedCurrentVersion || got.CurrentIsDev {
		t.Fatalf("Check = %#v, want unsupported non-dev version", got)
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
