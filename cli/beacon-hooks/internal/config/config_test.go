package config

import (
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
