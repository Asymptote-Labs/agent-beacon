package cmd

import (
	"path/filepath"
	"testing"
)

func TestGrokPromptSubmitRecordsPromptAndRawPayload(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hookEventName": "user_prompt_submit",
		"sessionId":     "grok-session-1",
		"workspaceRoot": "/tmp/grok-project",
		"prompt":        "summarize token=grok-secret",
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "grok" {
		t.Fatalf("harness.name = %q, want grok", harness)
	}
	if got := event["prompt"].(map[string]interface{})["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if got := event["session"].(map[string]interface{})["working_directory"]; got != "/tmp/grok-project" {
		t.Fatalf("working_directory = %q, want workspace root", got)
	}
	if _, ok := event["raw"].(map[string]interface{})["grok"]; !ok {
		t.Fatalf("raw.grok payload missing: %#v", event["raw"])
	}
}

func TestGrokMetadataRetentionOmitsPromptAndRawPayload(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hookEventName": "user_prompt_submit",
		"sessionId":     "grok-session-1",
		"prompt":        "secret prompt",
	})
	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt: %#v", event["prompt"])
	}
	raw := event["raw"].(map[string]interface{})
	if _, ok := raw["grok"]; ok {
		t.Fatalf("metadata retention should omit raw grok payload: %#v", raw)
	}
}

func TestGrokPreToolRecordsToolAndAllows(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"hookEventName": "pre_tool_use",
		"sessionId":     "grok-session-1",
		"workspaceRoot": "/tmp/grok-project",
		"toolName":      "run_terminal_command",
		"toolInput": map[string]interface{}{
			"command": "npm test",
		},
	})
	if got := out["decision"]; got != "allow" {
		t.Fatalf("decision = %q, want allow", got)
	}
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	tool := event["tool"].(map[string]interface{})
	if tool["name"] != "run_terminal_command" || tool["command"] != "npm test" {
		t.Fatalf("tool fields = %#v, want name and command", tool)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "npm test" {
		t.Fatalf("command = %q, want npm test", command)
	}
}

func TestGrokPostToolMapsCommandAndFailure(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hookEventName": "post_tool_use",
		"sessionId":     "grok-session-1",
		"toolName":      "run_terminal_command",
		"toolInput": map[string]interface{}{
			"command": "go test ./...",
		},
	})
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", action)
	}

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hookEventName": "post_tool_use_failure",
		"sessionId":     "grok-session-1",
		"toolName":      "unknown_tool",
	})
	event = lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.failed" {
		t.Fatalf("failure event.action = %q, want tool.failed", action)
	}
	if sev := event["severity"]; sev != "high" {
		t.Fatalf("failure severity = %q, want high", sev)
	}

	// Known tool failures must also be recorded as tool.failed with high severity.
	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hookEventName": "post_tool_use_failure",
		"sessionId":     "grok-session-1",
		"toolName":      "run_terminal_command",
		"toolInput": map[string]interface{}{
			"command": "exit 1",
		},
	})
	event = lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.failed" {
		t.Fatalf("known tool failure event.action = %q, want tool.failed", action)
	}
	if sev := event["severity"]; sev != "high" {
		t.Fatalf("known tool failure severity = %q, want high", sev)
	}
}

func TestGrokCwdFallsBackToEnvironment(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("GROK_WORKSPACE_ROOT", "/tmp/grok-env-project")

	runHookWithInput(t, runSessionStart, map[string]interface{}{
		"hookEventName": "session_start",
		"sessionId":     "grok-session-1",
	})
	event := lastEndpointEvent(t, logPath)
	if got := event["session"].(map[string]interface{})["working_directory"]; got != "/tmp/grok-env-project" {
		t.Fatalf("working_directory = %q, want env workspace root", got)
	}
}
