package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// hookFirstPartContent reads the text out of a gen_ai messages value written by a
// hook, the way a reader of the log would.
func hookFirstPartContent(t *testing.T, messages interface{}) string {
	t.Helper()
	list, ok := messages.([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("messages = %#v, want a non-empty list", messages)
	}
	message, ok := list[0].(map[string]interface{})
	if !ok {
		t.Fatalf("message = %#v, want a map", list[0])
	}
	parts, ok := message["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		t.Fatalf("parts = %#v, want a non-empty list", message["parts"])
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("part = %#v, want a map", parts[0])
	}
	content, _ := part["content"].(string)
	return content
}

func TestPromptSubmitRecordsRetainedPromptContent(t *testing.T) {
	// This path serves Claude Code, Cursor and the rest of the hook runtimes. It
	// retained the prompt with nothing saying so and without the semconv message
	// shape the Cline, opencode and Pi mappers already wrote.
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	prompt := "explain the dedupe key"
	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id": "prompt-content-session",
		"prompt":     prompt,
	})

	event := lastEndpointEvent(t, logPath)
	if got := event["prompt"].(map[string]interface{})["text"]; got != prompt {
		t.Fatalf("prompt.text = %q, want %q", got, prompt)
	}
	genAI, ok := event["gen_ai"].(map[string]interface{})
	if !ok {
		t.Fatalf("gen_ai missing: %#v", event)
	}
	input, ok := genAI["input"].(map[string]interface{})
	if !ok {
		t.Fatalf("gen_ai.input missing: %#v", genAI)
	}
	if got := hookFirstPartContent(t, input["messages"]); got != prompt {
		t.Fatalf("input message text = %q, want %q", got, prompt)
	}

	content, ok := event["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("content marker missing: %#v", event)
	}
	sum := sha256.Sum256([]byte(prompt))
	if content["hash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("content hash = %v, want %s", content["hash"], hex.EncodeToString(sum[:]))
	}
	if content["bytes"] != float64(len(prompt)) || content["included"] != true {
		t.Fatalf("content marker = %#v, want the retained prompt described", content)
	}
	if content["retention"] != asymptoteobserve.ContentRetentionFull {
		t.Fatalf("content retention = %v, want full", content["retention"])
	}
}

func TestPromptSubmitWithoutAPromptWritesNoContentMarker(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id": "no-prompt-session",
	})

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["content"]; ok {
		t.Fatalf("no prompt was retained, so no marker belongs on the event: %#v", event["content"])
	}
	if _, ok := event["gen_ai"]; ok {
		t.Fatalf("no prompt was retained, so no input messages belong on the event: %#v", event["gen_ai"])
	}
}

func TestCursorShellExecutionRecordsRetainedOutput(t *testing.T) {
	// command.output has always been written here and was never declared in the
	// schema, so it reached the log and no reader could see it. It is retained
	// content, so it takes a marker like every other retained field.
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	output := "PASS\nok  github.com/example/pkg  0.01s\n"
	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "afterShellExecution",
		"conversation_id": "shell-content-session",
		"command":         "go test ./...",
		"output":          output,
	})

	event := lastEndpointEvent(t, logPath)
	command, ok := event["command"].(map[string]interface{})
	if !ok {
		t.Fatalf("command field missing: %#v", event)
	}
	if command["output"] != output {
		t.Fatalf("command.output = %q, want %q", command["output"], output)
	}
	content, ok := event["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("content marker missing: %#v", event)
	}
	if content["bytes"] != float64(len(output)) || content["included"] != true {
		t.Fatalf("content marker = %#v, want the retained output described", content)
	}
}

func TestCursorShellExecutionWithoutOutputWritesNoContentMarker(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "afterShellExecution",
		"conversation_id": "shell-no-output-session",
		"command":         "true",
	})

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["content"]; ok {
		t.Fatalf("nothing was retained, so no marker belongs on the event: %#v", event["content"])
	}
}

func TestDiffFieldsMarkRetainedDiffContent(t *testing.T) {
	diff := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	fields := diffFields("/repo/main.go", diff)

	file, ok := fields["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("file field missing: %#v", fields)
	}
	if file["diff"] != diff {
		t.Fatalf("file.diff = %v, want the constructed diff", file["diff"])
	}
	content, ok := fields["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("content marker missing: %#v", fields)
	}
	sum := sha256.Sum256([]byte(diff))
	if content["hash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("content hash = %v, want %s", content["hash"], hex.EncodeToString(sum[:]))
	}
	// diff_hash and diff_bytes already describe the original diff; the marker must
	// agree with them rather than restate them differently.
	if file["diff_hash"] != content["hash"] || file["diff_bytes"] != content["bytes"] {
		t.Fatalf("diff metadata and content marker disagree: file=%#v content=%#v", file, content)
	}
}

func TestDiffFieldsWithoutADiffWriteNoContentMarker(t *testing.T) {
	fields := diffFields("/repo/main.go", "")
	if _, ok := fields["content"]; ok {
		t.Fatalf("no diff was retained, so no marker belongs in the fields: %#v", fields)
	}
	if _, ok := fields["file"]; !ok {
		t.Fatalf("file metadata should still be recorded without a diff: %#v", fields)
	}
}

func TestRetainedContentFieldsUseTheHookWritersLimit(t *testing.T) {
	// The hook logger sanitizes the whole event map at DefaultStringLimit, so that
	// is the limit "truncated" has to mean on this path.
	short := strings.Repeat("z", asymptoteobserve.DefaultStringLimit)
	if fields := retainedContentFields(short); fields["truncated"] != nil {
		t.Fatalf("text at the limit should not be marked truncated: %#v", fields)
	}
	long := strings.Repeat("z", asymptoteobserve.DefaultStringLimit+1)
	if fields := retainedContentFields(long); fields["truncated"] != true {
		t.Fatalf("text over the limit should be marked truncated: %#v", fields)
	}
}
