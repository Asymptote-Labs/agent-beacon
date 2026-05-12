package version

import (
	"strings"
	"testing"
)

func TestGetVersion(t *testing.T) {
	// Store original values
	origVersion := Version
	defer func() { Version = origVersion }()

	tests := []struct {
		name     string
		version  string
		expected string
	}{
		{
			name:     "default dev version",
			version:  "dev",
			expected: "dev",
		},
		{
			name:     "semantic version",
			version:  "v1.2.3",
			expected: "v1.2.3",
		},
		{
			name:     "version with prerelease",
			version:  "v1.0.0-beta.1",
			expected: "v1.0.0-beta.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			got := GetVersion()
			if got != tt.expected {
				t.Errorf("GetVersion() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetFullVersion(t *testing.T) {
	// Store original values
	origVersion := Version
	origCommit := GitCommit
	origDate := BuildDate
	defer func() {
		Version = origVersion
		GitCommit = origCommit
		BuildDate = origDate
	}()

	tests := []struct {
		name      string
		version   string
		commit    string
		date      string
		wantParts []string
	}{
		{
			name:      "default values",
			version:   "dev",
			commit:    "unknown",
			date:      "unknown",
			wantParts: []string{"dev", "unknown", "built on", "unknown"},
		},
		{
			name:      "release values",
			version:   "v1.0.0",
			commit:    "abc1234",
			date:      "2025-01-15",
			wantParts: []string{"v1.0.0", "abc1234", "built on", "2025-01-15"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			GitCommit = tt.commit
			BuildDate = tt.date

			got := GetFullVersion()

			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("GetFullVersion() = %q, missing part %q", got, part)
				}
			}
		})
	}
}

func TestGetFullVersion_Format(t *testing.T) {
	// Store original values
	origVersion := Version
	origCommit := GitCommit
	origDate := BuildDate
	defer func() {
		Version = origVersion
		GitCommit = origCommit
		BuildDate = origDate
	}()

	Version = "v1.2.3"
	GitCommit = "abc1234"
	BuildDate = "2025-01-15"

	got := GetFullVersion()
	expected := "v1.2.3 (abc1234) built on 2025-01-15"

	if got != expected {
		t.Errorf("GetFullVersion() = %q, want %q", got, expected)
	}
}

func TestDefaultValues(t *testing.T) {
	// These tests verify the default values are set correctly
	// In a real build, these would be overridden by ldflags
	// but in tests, we should see the defaults

	// Note: We can't actually test defaults here because other tests
	// may have modified them. Instead, verify the variables are non-empty.
	if Version == "" {
		t.Error("Version should not be empty")
	}
	if GitCommit == "" {
		t.Error("GitCommit should not be empty")
	}
	if BuildDate == "" {
		t.Error("BuildDate should not be empty")
	}
}
