package cmd

import (
	"path/filepath"
	"testing"
)

func TestIsFileEditTool(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		toolName string
		want     bool
	}{
		// Claude tools
		{"claude Write", "claude", "Write", true},
		{"claude Edit", "claude", "Edit", true},
		{"claude MultiEdit", "claude", "MultiEdit", true},
		{"claude Read (not edit)", "claude", "Read", false},

		// Copilot tools
		{"copilot edit tool", "copilot", "copilot_insertEdit", true},
		{"copilot write tool", "copilot", "copilot_createFile", true},
		{"copilot patch tool", "copilot", "apply_patch", true},
		{"copilot read (not edit)", "copilot", "readFile", false},

		// Factory tools
		{"factory Write", "factory", "Write", true},
		{"factory Edit", "factory", "Edit", true},
		{"factory MultiEdit", "factory", "MultiEdit", true},
		{"factory Create", "factory", "Create", true},
		{"factory Read (not edit)", "factory", "Read", false},

		// Devin tools
		{"devin edit", "devin", "edit", true},
		{"devin write", "devin", "write", true},
		{"devin exec (not edit)", "devin", "exec", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isFileEditTool(tt.platform, tt.toolName)
			if got != tt.want {
				t.Errorf("isFileEditTool(%q, %q) = %v, want %v", tt.platform, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestRunPostToolEmitsDevinFileModifiedEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"cwd":       "/repo",
		"tool_name": "edit",
		"tool_input": map[string]interface{}{
			"file_path": "/repo/main.go",
			"old_str":   "old",
			"new_str":   "new token=devin-secret",
		},
		"tool_response": map[string]interface{}{"success": true},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "devin" {
		t.Fatalf("harness = %q, want devin", harness)
	}
	if session, ok := event["session"].(map[string]interface{}); ok {
		if _, hasID := session["id"]; hasID {
			t.Fatalf("devin file event should not include empty session id: %#v", session)
		}
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/repo/main.go" {
		t.Fatalf("file = %#v, want /repo/main.go", file)
	}
	if _, ok := file["diff_hash"]; !ok {
		t.Fatalf("file diff_hash missing: %#v", file)
	}
}

func TestRunPostToolEmitsFactoryFileModifiedEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "factory"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id": "factory-session",
		"cwd":        "/repo",
		"model":      "opus",
		"tool_name":  "Edit",
		"tool_input": map[string]interface{}{
			"file_path": "/repo/main.go",
			"old_str":   "old",
			"new_str":   "new token=factory-secret",
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := event["message"]; got != "File edit observed" {
		t.Fatalf("message = %q, want file edit", got)
	}
	if harness := event["harness"].(map[string]interface{}); harness["name"] != "factory" {
		t.Fatalf("harness = %#v, want factory", harness)
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/repo/main.go" || file["operation"] != "modify" {
		t.Fatalf("file = %#v, want main.go modify", file)
	}
	if _, ok := file["diff_hash"]; !ok {
		t.Fatalf("file diff_hash missing: %#v", file)
	}
	if diff, _ := file["diff"].(string); diff == "" || diff == "new token=factory-secret" {
		t.Fatalf("expected redacted diff content, got %q", diff)
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "factory-session" {
		t.Fatalf("session = %#v, want factory-session", session)
	}
}

func TestResolveToolInput(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]interface{}
		want  bool // whether result is non-nil
	}{
		{
			name:  "tool_input map",
			input: map[string]interface{}{"tool_input": map[string]interface{}{"file_path": "/test.py"}},
			want:  true,
		},
		{
			name:  "toolArgs string JSON",
			input: map[string]interface{}{"toolArgs": `{"file_path": "/test.py"}`},
			want:  true,
		},
		{
			name:  "toolArgs map",
			input: map[string]interface{}{"toolArgs": map[string]interface{}{"file_path": "/test.py"}},
			want:  true,
		},
		{
			name:  "empty input",
			input: map[string]interface{}{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolInput(tt.input)
			if tt.want && got == nil {
				t.Error("resolveToolInput() = nil, want non-nil")
			}
			if !tt.want && got != nil {
				t.Errorf("resolveToolInput() = %v, want nil", got)
			}
		})
	}
}
