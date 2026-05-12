package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetStateDir(t *testing.T) {
	tests := []struct {
		platform string
		wantDir  string
	}{
		{"claude", ClaudeDir},
		{"copilot", CopilotDir},
		{"cursor", CursorDir},
		{"unknown", ClaudeDir}, // defaults to claude
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			got := GetStateDir(tt.platform)
			if got != tt.wantDir {
				t.Errorf("GetStateDir(%q) = %q, want %q", tt.platform, got, tt.wantDir)
			}
		})
	}
}

func TestGetLogFile(t *testing.T) {
	tests := []struct {
		platform string
		wantBase string
	}{
		{"claude", "hooks.log"},
		{"copilot", "hooks.log"},
		{"cursor", "hooks.log"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			got := GetLogFile(tt.platform)
			if filepath.Base(got) != tt.wantBase {
				t.Errorf("GetLogFile(%q) base = %q, want %q", tt.platform, filepath.Base(got), tt.wantBase)
			}
		})
	}
}

func TestGetSessionLogDir(t *testing.T) {
	tests := []struct {
		platform string
		wantBase string
	}{
		{"claude", "logs"},
		{"copilot", "logs"},
		{"cursor", "logs"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			got := GetSessionLogDir(tt.platform)
			if filepath.Base(got) != tt.wantBase {
				t.Errorf("GetSessionLogDir(%q) base = %q, want %q", tt.platform, filepath.Base(got), tt.wantBase)
			}
			// Parent should be the platform state dir
			if filepath.Dir(got) != GetStateDir(tt.platform) {
				t.Errorf("GetSessionLogDir(%q) parent = %q, want %q", tt.platform, filepath.Dir(got), GetStateDir(tt.platform))
			}
		})
	}
}

func TestGetSessionLogFile(t *testing.T) {
	tests := []struct {
		platform  string
		sessionID string
		wantFile  string
	}{
		{"claude", "abc-123", "abc-123.log"},
		{"copilot", "sess-456", "sess-456.log"},
		{"cursor", "conv-789", "conv-789.log"},
	}

	for _, tt := range tests {
		t.Run(tt.platform+"_"+tt.sessionID, func(t *testing.T) {
			got := GetSessionLogFile(tt.platform, tt.sessionID)
			if filepath.Base(got) != tt.wantFile {
				t.Errorf("GetSessionLogFile(%q, %q) base = %q, want %q", tt.platform, tt.sessionID, filepath.Base(got), tt.wantFile)
			}
			// Parent should be the session log dir
			if filepath.Dir(got) != GetSessionLogDir(tt.platform) {
				t.Errorf("GetSessionLogFile(%q, %q) parent = %q, want %q", tt.platform, tt.sessionID, filepath.Dir(got), GetSessionLogDir(tt.platform))
			}
		})
	}
}

func TestCursorDirPath(t *testing.T) {
	// CursorDir should be ~/.beacon/cursor
	if filepath.Base(CursorDir) != "cursor" {
		t.Errorf("CursorDir should end with 'cursor', got %q", CursorDir)
	}
	if filepath.Base(filepath.Dir(CursorDir)) != ".beacon" {
		t.Errorf("CursorDir parent should be '.beacon', got %q", filepath.Dir(CursorDir))
	}
}

func TestIsSecureByDesignEnabled_PlatformSpecific(t *testing.T) {
	tmpDir := t.TempDir()

	origCursorDir := CursorDir
	origBeaconDir := BeaconDir
	CursorDir = filepath.Join(tmpDir, "cursor")
	BeaconDir = tmpDir
	defer func() {
		CursorDir = origCursorDir
		BeaconDir = origBeaconDir
	}()

	os.MkdirAll(filepath.Join(tmpDir, "cursor"), 0755)

	// No config file → false
	if IsSecureByDesignEnabled("cursor") {
		t.Error("should be false when no config exists")
	}

	// Platform config enabled
	os.WriteFile(filepath.Join(tmpDir, "cursor", "config.json"), []byte(`{"secure_by_design": true}`), 0644)
	if !IsSecureByDesignEnabled("cursor") {
		t.Error("should be true when platform config has secure_by_design: true")
	}

	// Platform-specific false overrides global true
	os.WriteFile(filepath.Join(tmpDir, "cursor", "config.json"), []byte(`{"secure_by_design": false}`), 0644)
	os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte(`{"secure_by_design": true}`), 0644)
	if IsSecureByDesignEnabled("cursor") {
		t.Error("platform-specific false should take precedence over global true")
	}
}

func TestIsSecureByDesignEnabled_GlobalIgnored(t *testing.T) {
	tmpDir := t.TempDir()

	origClaudeDir := ClaudeDir
	origBeaconDir := BeaconDir
	ClaudeDir = filepath.Join(tmpDir, "claude")
	BeaconDir = tmpDir
	defer func() {
		ClaudeDir = origClaudeDir
		BeaconDir = origBeaconDir
	}()

	// No platform config, global has SbD enabled → should NOT fall back (global deprecated)
	os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte(`{"secure_by_design": true}`), 0644)
	if IsSecureByDesignEnabled("claude") {
		t.Error("should ignore global config, only platform-specific config matters")
	}
}

func TestIsSecureByDesignEnabled_NoConfigFiles(t *testing.T) {
	tmpDir := t.TempDir()

	origClaudeDir := ClaudeDir
	origBeaconDir := BeaconDir
	ClaudeDir = filepath.Join(tmpDir, "claude")
	BeaconDir = tmpDir
	defer func() {
		ClaudeDir = origClaudeDir
		BeaconDir = origBeaconDir
	}()

	// No config files at all → false
	if IsSecureByDesignEnabled("claude") {
		t.Error("should be false when no config files exist")
	}
}

func TestIsSecureByDesignEnabled_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	origCursorDir := CursorDir
	origBeaconDir := BeaconDir
	CursorDir = filepath.Join(tmpDir, "cursor")
	BeaconDir = tmpDir
	defer func() {
		CursorDir = origCursorDir
		BeaconDir = origBeaconDir
	}()

	os.MkdirAll(filepath.Join(tmpDir, "cursor"), 0755)

	// Invalid JSON in platform config → false (no fallback)
	os.WriteFile(filepath.Join(tmpDir, "cursor", "config.json"), []byte(`not json`), 0644)
	if IsSecureByDesignEnabled("cursor") {
		t.Error("should be false when platform config has invalid JSON")
	}
}
