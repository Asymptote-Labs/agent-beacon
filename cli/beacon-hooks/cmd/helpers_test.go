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
			name: "cursor ignores session_id",
			input: map[string]interface{}{
				"session_id":      "should-be-ignored",
				"conversation_id": "conv_abc123",
			},
			platform: "cursor",
			want:     "conv_abc123",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSessionID(tt.input, "cline"); got != tt.want {
				t.Errorf("resolveSessionID(%v, \"cline\") = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Kiro, Kimi Code, DeepSeek Harness, Muse Code and Qwen Code spell the envelope exactly as Claude
// Code does -- `session_id` and `cwd` -- and have no envelope readers of their own: every event's
// session.id and session.working_directory come from the shared default cases. Driven through the
// hook process per runtime, because a branch added for one of them in those readers would take that
// runtime's session identity with it silently. Muse's transcript_path is documented as nullable,
// and a null must not take the session or the workspace down with it.
func TestClaudeShapedRuntimesTakeSessionAndWorkspaceFromTheEnvelope(t *testing.T) {
	for _, platform := range []string{kiroPlatform, kimiPlatform, dshPlatform, "muse", "qwen"} {
		t.Run(platform, func(t *testing.T) {
			logPath, _ := runHookProcess(t, map[string]interface{}{
				"hook_event_name": "PreToolUse",
				"session_id":      "envelope-" + platform,
				"cwd":             "/repo/" + platform,
				"transcript_path": nil,
				"tool_name":       "read",
				"tool_input":      map[string]interface{}{"path": "/repo/main.go"},
			}, "--platform", platform, "pre-tool")
			event := lastEndpointEvent(t, logPath)
			if got := leaf(event, "session", "id"); got != "envelope-"+platform {
				t.Errorf("session.id = %q, want envelope-%s", got, platform)
			}
			if got := leaf(event, "session", "working_directory"); got != "/repo/"+platform {
				t.Errorf("session.working_directory = %q, want /repo/%s", got, platform)
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
			name: "cursor cwd takes precedence over workspace_roots",
			input: map[string]interface{}{
				"cwd":             "/projects/myapp",
				"workspace_roots": []interface{}{"/workspace/root"},
			},
			platform: "cursor",
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
			name: "cline takes the first of several roots",
			input: map[string]interface{}{
				"workspaceRoots": []interface{}{"/workspace/first", "/workspace/second"},
			},
			platform: "cline",
			want:     "/workspace/first",
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
			name:     "claude uses cwd field",
			input:    map[string]interface{}{"cwd": "/projects/myapp"},
			platform: "claude",
			want:     "/projects/myapp",
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
		{"skip empty, return second", []string{"key2", "key3"}, "value3"},
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
