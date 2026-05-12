package cmd

import (
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
