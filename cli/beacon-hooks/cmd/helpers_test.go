package cmd

import (
	"testing"
)

func TestResolveSessionID_Cursor(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		platform string
		want     string
	}{
		{
			name: "cursor uses conversation_id",
			input: map[string]interface{}{
				"conversation_id": "conv_abc123",
			},
			platform: "cursor",
			want:     "conv_abc123",
		},
		{
			name: "cursor ignores session_id",
			input: map[string]interface{}{
				"session_id":      "should-be-ignored",
				"conversation_id": "conv_abc123",
			},
			platform: "cursor",
			want:     "conv_abc123",
		},
		{
			name:     "cursor returns empty when no conversation_id",
			input:    map[string]interface{}{},
			platform: "cursor",
			want:     "",
		},
		{
			name: "claude uses session_id",
			input: map[string]interface{}{
				"session_id": "sess_123",
			},
			platform: "claude",
			want:     "sess_123",
		},
		{
			name: "copilot prefers transcript path UUID",
			input: map[string]interface{}{
				"transcriptPath": "/path/to/transcripts/ff2d7803-5799-4f18-83f0-3633b2c11809.jsonl",
				"sessionId":      "vscode-session-id",
			},
			platform: "copilot",
			want:     "ff2d7803-5799-4f18-83f0-3633b2c11809",
		},
		{
			name: "hermes uses top-level session_id",
			input: map[string]interface{}{
				"session_id": "hermes-sess-1",
			},
			platform: "hermes",
			want:     "hermes-sess-1",
		},
		{
			name: "hermes uses session_key",
			input: map[string]interface{}{
				"session_key": "hermes-key-1",
			},
			platform: "hermes",
			want:     "hermes-key-1",
		},
		{
			name: "hermes reads session_id from extra",
			input: map[string]interface{}{
				"extra": map[string]interface{}{
					"session_id": "hermes-extra-sess",
				},
			},
			platform: "hermes",
			want:     "hermes-extra-sess",
		},
		{
			name: "hermes reads session_key from extra",
			input: map[string]interface{}{
				"extra": map[string]interface{}{
					"session_key": "hermes-extra-key",
				},
			},
			platform: "hermes",
			want:     "hermes-extra-key",
		},
		{
			name: "hermes top-level session_id takes precedence over extra",
			input: map[string]interface{}{
				"session_id": "top-level",
				"extra": map[string]interface{}{
					"session_id": "from-extra",
				},
			},
			platform: "hermes",
			want:     "top-level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSessionID(tt.input, tt.platform)
			if got != tt.want {
				t.Errorf("resolveSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSessionIDWithTranscript_Cursor(t *testing.T) {
	tests := []struct {
		name               string
		input              map[string]interface{}
		platform           string
		wantSessionID      string
		wantTranscriptPath string
	}{
		{
			name: "cursor extracts both conversation_id and transcript_path",
			input: map[string]interface{}{
				"conversation_id": "conv_abc123",
				"transcript_path": "/path/to/transcript.jsonl",
			},
			platform:           "cursor",
			wantSessionID:      "conv_abc123",
			wantTranscriptPath: "/path/to/transcript.jsonl",
		},
		{
			name: "cursor with only conversation_id",
			input: map[string]interface{}{
				"conversation_id": "conv_abc123",
			},
			platform:           "cursor",
			wantSessionID:      "conv_abc123",
			wantTranscriptPath: "",
		},
		{
			name:               "cursor with neither field",
			input:              map[string]interface{}{},
			platform:           "cursor",
			wantSessionID:      "",
			wantTranscriptPath: "",
		},
		{
			name: "claude extracts session_id and transcript_path",
			input: map[string]interface{}{
				"session_id":      "sess_123",
				"transcript_path": "/path/to/transcript.jsonl",
			},
			platform:           "claude",
			wantSessionID:      "sess_123",
			wantTranscriptPath: "/path/to/transcript.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID, transcriptPath := resolveSessionIDWithTranscript(tt.input, tt.platform)
			if sessionID != tt.wantSessionID {
				t.Errorf("sessionID = %q, want %q", sessionID, tt.wantSessionID)
			}
			if transcriptPath != tt.wantTranscriptPath {
				t.Errorf("transcriptPath = %q, want %q", transcriptPath, tt.wantTranscriptPath)
			}
		})
	}
}

func TestResolveCwd(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		platform string
		envCwd   string // CURSOR_PROJECT_DIR env var
		want     string
	}{
		{
			name:     "cursor uses cwd field",
			input:    map[string]interface{}{"cwd": "/projects/myapp"},
			platform: "cursor",
			want:     "/projects/myapp",
		},
		{
			name: "cursor falls back to workspace_roots",
			input: map[string]interface{}{
				"workspace_roots": []interface{}{"/workspace/root"},
			},
			platform: "cursor",
			want:     "/workspace/root",
		},
		{
			name:     "cursor falls back to CURSOR_PROJECT_DIR env",
			input:    map[string]interface{}{},
			platform: "cursor",
			envCwd:   "/env/project/dir",
			want:     "/env/project/dir",
		},
		{
			name:     "cursor returns empty when no sources",
			input:    map[string]interface{}{},
			platform: "cursor",
			want:     "",
		},
		{
			name: "cursor cwd takes precedence over workspace_roots",
			input: map[string]interface{}{
				"cwd":             "/projects/myapp",
				"workspace_roots": []interface{}{"/workspace/root"},
			},
			platform: "cursor",
			want:     "/projects/myapp",
		},
		{
			name:     "claude uses cwd field",
			input:    map[string]interface{}{"cwd": "/projects/myapp"},
			platform: "claude",
			want:     "/projects/myapp",
		},
		{
			name:     "claude returns empty when no cwd",
			input:    map[string]interface{}{},
			platform: "claude",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set/unset CURSOR_PROJECT_DIR env var
			if tt.envCwd != "" {
				t.Setenv("CURSOR_PROJECT_DIR", tt.envCwd)
			} else {
				t.Setenv("CURSOR_PROJECT_DIR", "")
			}
			got := resolveCwd(tt.input, tt.platform)
			if got != tt.want {
				t.Errorf("resolveCwd() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetFirstStr(t *testing.T) {
	input := map[string]interface{}{
		"key1": "value1",
		"key2": "",
		"key3": "value3",
	}

	tests := []struct {
		name string
		keys []string
		want string
	}{
		{"first key found", []string{"key1"}, "value1"},
		{"skip empty, return second", []string{"key2", "key3"}, "value3"},
		{"no matching key", []string{"nonexistent"}, ""},
		{"first non-empty wins", []string{"key1", "key3"}, "value1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getFirstStr(input, tt.keys...)
			if got != tt.want {
				t.Errorf("getFirstStr() = %q, want %q", got, tt.want)
			}
		})
	}
}
