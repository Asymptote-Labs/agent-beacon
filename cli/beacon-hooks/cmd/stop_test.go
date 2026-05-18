package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformToTranscriptName(t *testing.T) {
	tests := []struct {
		platform string
		want     string
	}{
		{"claude", "claude_code"},
		{"copilot", "copilot"},
		{"cursor", "cursor"},
		{"factory", "factory"},
		{"unknown", "claude_code"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			got := platformToTranscriptName(tt.platform)
			if got != tt.want {
				t.Errorf("platformToTranscriptName(%q) = %q, want %q", tt.platform, got, tt.want)
			}
		})
	}
}

func TestExtractMessagesFromFactoryTranscript(t *testing.T) {
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "factory.jsonl")
	content := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Please edit the file"}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit"}]}}
{"type":"message","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Done editing."}]}}
`
	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	messages := extractMessagesFromFactoryTranscript(transcriptPath)
	if len(messages) != 2 {
		t.Fatalf("expected 2 text messages, got %d: %#v", len(messages), messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "Please edit the file" {
		t.Fatalf("unexpected first message: %#v", messages[0])
	}
	if messages[1]["role"] != "assistant" || messages[1]["content"] != "Done editing." {
		t.Fatalf("unexpected second message: %#v", messages[1])
	}
}

func TestExtractMessagesFromCursorTranscript(t *testing.T) {
	// Create a temp transcript file matching Cursor's format
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	content := `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nPlease fix the bug\n</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"I'll fix the bug now."}]}}
{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nThanks!\n</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"You're welcome!"},{"type":"text","text":"Let me know if you need anything else."}]}}
`

	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	messages := extractMessagesFromCursorTranscript(transcriptPath)

	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d", len(messages))
	}

	// Check first user message
	if messages[0]["role"] != "user" {
		t.Errorf("Message 0 role = %q, want 'user'", messages[0]["role"])
	}
	if messages[0]["content"] != "Please fix the bug" {
		t.Errorf("Message 0 content = %q, want 'Please fix the bug'", messages[0]["content"])
	}

	// Check first assistant message
	if messages[1]["role"] != "assistant" {
		t.Errorf("Message 1 role = %q, want 'assistant'", messages[1]["role"])
	}
	if messages[1]["content"] != "I'll fix the bug now." {
		t.Errorf("Message 1 content = %q", messages[1]["content"])
	}

	// Check multi-block assistant message (blocks joined with space)
	if messages[3]["role"] != "assistant" {
		t.Errorf("Message 3 role = %q, want 'assistant'", messages[3]["role"])
	}
	expected := "You're welcome! Let me know if you need anything else."
	if messages[3]["content"] != expected {
		t.Errorf("Message 3 content = %q, want %q", messages[3]["content"], expected)
	}
}

func TestExtractMessagesFromCursorTranscript_SkipsNonTextBlocks(t *testing.T) {
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Include a non-text block (e.g., tool_use) that should be skipped
	content := `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"write_file"},{"type":"text","text":"Done editing."}]}}
`

	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	messages := extractMessagesFromCursorTranscript(transcriptPath)

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if messages[0]["content"] != "Done editing." {
		t.Errorf("content = %q, want 'Done editing.'", messages[0]["content"])
	}
}

func TestExtractMessagesFromCursorTranscript_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	if err := os.WriteFile(transcriptPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	messages := extractMessagesFromCursorTranscript(transcriptPath)
	if len(messages) != 0 {
		t.Errorf("Expected 0 messages from empty file, got %d", len(messages))
	}
}

func TestExtractMessagesFromCursorTranscript_FileNotFound(t *testing.T) {
	messages := extractMessagesFromCursorTranscript("/nonexistent/path.jsonl")
	if messages != nil {
		t.Errorf("Expected nil for nonexistent file, got %v", messages)
	}
}

func TestExtractMessagesFromClaudeTranscript_IncludesStopHookFeedback(t *testing.T) {
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Simulate a Claude Code transcript with:
	// 1. A normal user message
	// 2. An assistant response
	// 3. A stop hook feedback message (isMeta=true, should be included)
	// 4. A system stop_hook_summary (should be excluded - type "system")
	// 5. A Conductor meta message (isMeta=true, should be excluded)
	content := `{"type":"user","message":{"role":"user","content":"write a vulnerable file"},"uuid":"u1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Here is the file."}]},"uuid":"a1"}
{"type":"user","message":{"role":"user","content":"Stop hook feedback:\n## Security Vulnerabilities Detected\n\nPlease fix these issues."},"isMeta":true,"uuid":"u2"}
{"type":"system","subtype":"stop_hook_summary","hookErrors":["## Security Vulnerabilities Detected"],"uuid":"s1"}
{"type":"user","message":{"role":"user","content":"<local-command-caveat>some conductor metadata</local-command-caveat>"},"isMeta":true,"uuid":"u3"}
`

	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	messages := extractMessagesFromClaudeTranscript(transcriptPath)

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (user + assistant + stop hook feedback), got %d", len(messages))
	}

	// First message: normal user message
	if messages[0]["role"] != "user" || messages[0]["content"] != "write a vulnerable file" {
		t.Errorf("Message 0: unexpected content %v", messages[0])
	}

	// Second message: assistant response
	if messages[1]["role"] != "assistant" || messages[1]["content"] != "Here is the file." {
		t.Errorf("Message 1: unexpected content %v", messages[1])
	}

	// Third message: stop hook feedback (should NOT be filtered despite isMeta=true)
	content3, _ := messages[2]["content"].(string)
	if messages[2]["role"] != "user" || content3 == "" {
		t.Errorf("Message 2: expected stop hook feedback as user message, got %v", messages[2])
	}
	if !strings.Contains(content3, "Security Vulnerabilities Detected") {
		t.Errorf("Message 2: expected security feedback content, got %q", content3)
	}
}

func TestExtractMessages_DispatchesByPlatform(t *testing.T) {
	// Create a Cursor-format transcript
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	content := `{"role":"user","message":{"content":[{"type":"text","text":"hello"}]}}
`

	if err := os.WriteFile(transcriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	// Cursor dispatch should work
	messages := extractMessages(transcriptPath, "cursor")
	if len(messages) != 1 {
		t.Errorf("cursor dispatch: expected 1 message, got %d", len(messages))
	}

	// Same file through Claude dispatch should return 0 (different format)
	messages = extractMessages(transcriptPath, "claude")
	// Claude parser uses json.Decoder which may or may not parse this,
	// but it won't match the "type" field pattern, so 0 messages expected
	if len(messages) != 0 {
		t.Errorf("claude dispatch: expected 0 messages from cursor-format file, got %d", len(messages))
	}
}

func TestStripCursorXMLTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"user_query tags",
			"<user_query>\nPlease fix the bug\n</user_query>",
			"Please fix the bug",
		},
		{
			"no tags",
			"Just a plain message",
			"Just a plain message",
		},
		{
			"attached_files tags",
			"<user_query>\nFix this\n</user_query>\n<attached_files>\nfile.py\n</attached_files>",
			"Fix this\n\n\nfile.py",
		},
		{
			"git_diff tag",
			"<git_diff_from_branch_to_main>\n+new line\n</git_diff_from_branch_to_main>",
			"+new line",
		},
		{
			"empty after stripping",
			"<user_query>\n</user_query>",
			"",
		},
		{
			"preserves HTML tags in assistant content",
			"Use a <div> element with <span> styling",
			"Use a <div> element with <span> styling",
		},
		{
			"preserves think tags",
			"<think>\nLet me consider this\n</think>",
			"<think>\nLet me consider this\n</think>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCursorXMLTags(tt.input)
			if got != tt.want {
				t.Errorf("stripCursorXMLTags() = %q, want %q", got, tt.want)
			}
		})
	}
}
