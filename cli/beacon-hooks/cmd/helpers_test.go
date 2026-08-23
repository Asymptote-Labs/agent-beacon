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

// Cline names its unit of work a task, so the session identifier arrives as `taskId`. Getting this
// wrong does not fail loudly: every event still writes, with an empty session, and the runtime log
// silently loses the ability to group one Cline task's activity together.
func TestResolveSessionID_Cline(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]interface{}
		want  string
	}{
		{
			name:  "uses taskId",
			input: map[string]interface{}{"taskId": "task_abc123"},
			want:  "task_abc123",
		},
		{
			name:  "accepts snake_case task_id",
			input: map[string]interface{}{"task_id": "task_abc123"},
			want:  "task_abc123",
		},
		{
			name:  "taskId wins over session spellings",
			input: map[string]interface{}{"taskId": "task_abc123", "sessionId": "ignored"},
			want:  "task_abc123",
		},
		{
			name:  "falls back to sessionId",
			input: map[string]interface{}{"sessionId": "sess_abc123"},
			want:  "sess_abc123",
		},
		{
			name:  "empty when the payload carries neither",
			input: map[string]interface{}{},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSessionID(tt.input, "cline"); got != tt.want {
				t.Errorf("resolveSessionID(%v, \"cline\") = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Cline hooks get no transcript path. Asserted rather than assumed: the default branch would look
// for `transcript_path`, and a future reader could "fix" the missing value by adding a key that
// Cline never sends.
func TestResolveSessionIDWithTranscript_Cline(t *testing.T) {
	sessionID, transcriptPath := resolveSessionIDWithTranscript(
		map[string]interface{}{"taskId": "task_abc123", "transcript_path": "/not/sent/by/cline"},
		"cline",
	)
	if sessionID != "task_abc123" {
		t.Errorf("sessionID = %q, want %q", sessionID, "task_abc123")
	}
	if transcriptPath != "" {
		t.Errorf("transcriptPath = %q, want empty -- Cline does not expose a transcript to hooks", transcriptPath)
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
			name:     "cline uses cwd field",
			input:    map[string]interface{}{"cwd": "/projects/myapp"},
			platform: "cline",
			want:     "/projects/myapp",
		},
		{
			name: "cline falls back to workspaceRoots",
			input: map[string]interface{}{
				"workspaceRoots": []interface{}{"/workspace/root"},
			},
			platform: "cline",
			want:     "/workspace/root",
		},
		{
			name: "cline accepts snake_case workspace_roots",
			input: map[string]interface{}{
				"workspace_roots": []interface{}{"/workspace/root"},
			},
			platform: "cline",
			want:     "/workspace/root",
		},
		{
			name: "cline reads a workspace root object",
			input: map[string]interface{}{
				"workspaceRoots": []interface{}{
					map[string]interface{}{"path": "/workspace/root", "vcs": "git"},
				},
			},
			platform: "cline",
			want:     "/workspace/root",
		},
		{
			name: "cline takes the first of several roots",
			input: map[string]interface{}{
				"workspaceRoots": []interface{}{"/workspace/first", "/workspace/second"},
			},
			platform: "cline",
			want:     "/workspace/first",
		},
		{
			name: "cline cwd takes precedence over workspaceRoots",
			input: map[string]interface{}{
				"cwd":            "/projects/myapp",
				"workspaceRoots": []interface{}{"/workspace/root"},
			},
			platform: "cline",
			want:     "/projects/myapp",
		},
		{
			// A file:// URI is not a filesystem path. Empty is the correct answer: it degrades to
			// "no repository context" instead of writing a path that looks real and resolves to
			// nothing.
			name: "cline ignores a uri-only workspace root",
			input: map[string]interface{}{
				"workspaceRoots": []interface{}{
					map[string]interface{}{"uri": "file:///workspace/root"},
				},
			},
			platform: "cline",
			want:     "",
		},
		{
			name:     "cline returns empty when no sources",
			input:    map[string]interface{}{},
			platform: "cline",
			want:     "",
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
