package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestHermesPromptSubmitReadsExtraUserMessage(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "hermes"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hook_event_name": "pre_llm_call",
		"session_id":      "hermes-session",
		"cwd":             "/repo",
		"extra": map[string]interface{}{
			"user_message": "summarize token=prompt-secret",
			"model":        "nous/hermes",
		},
	})
	if len(out) != 0 {
		t.Fatalf("hermes prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "hermes" {
		t.Fatalf("harness = %q, want hermes", harness)
	}
	prompt := event["prompt"].(map[string]interface{})
	if got := prompt["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
}

func TestHermesPreToolEmitsObservedToolEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "hermes"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"hook_event_name": "pre_tool_call",
		"session_id":      "hermes-session",
		"cwd":             "/repo",
		"tool_name":       "terminal",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if len(out) != 0 {
		t.Fatalf("hermes pre-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "git status" {
		t.Fatalf("command = %q, want git status", command)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("Hermes pre_tool_call should not emit approval telemetry: %#v", event["approval"])
	}
}

func TestHermesPostToolEmitsCommandEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "hermes"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "post_tool_call",
		"session_id":      "hermes-session",
		"cwd":             "/repo",
		"tool_name":       "terminal",
		"tool_input": map[string]interface{}{
			"command": "go test ./...",
		},
		"extra": map[string]interface{}{
			"result":      `{"exit_code":0}`,
			"duration_ms": 123,
		},
	})
	if len(out) != 0 {
		t.Fatalf("hermes post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", action)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "go test ./..." {
		t.Fatalf("command = %q, want go test ./...", command)
	}
}

func TestHermesApprovalRequestAndResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "hermes"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "pre_approval_request",
		"session_id":      "hermes-session",
		"extra": map[string]interface{}{
			"command":     "rm -rf build",
			"description": "destructive command",
		},
	})
	requested := lastEndpointEvent(t, logPath)
	if action := requested["event"].(map[string]interface{})["action"]; action != "approval.requested" {
		t.Fatalf("request action = %q, want approval.requested", action)
	}
	if decision := requested["approval"].(map[string]interface{})["decision"]; decision != "requested" {
		t.Fatalf("request decision = %q, want requested", decision)
	}

	runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "post_approval_response",
		"session_id":      "hermes-session",
		"extra": map[string]interface{}{
			"command": "rm -rf build",
			"choice":  "deny",
		},
	})
	denied := lastEndpointEvent(t, logPath)
	if action := denied["event"].(map[string]interface{})["action"]; action != "approval.denied" {
		t.Fatalf("response action = %q, want approval.denied", action)
	}
	if decision := denied["approval"].(map[string]interface{})["decision"]; decision != "deny" {
		t.Fatalf("response decision = %q, want deny", decision)
	}
}

func TestHermesSubagentStopIncludesExtraMetadata(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "hermes"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, func(cmd *cobra.Command, args []string) {
		runSubagentLifecycle("subagent.stopped", "Subagent stopped")
	}, map[string]interface{}{
		"hook_event_name": "subagent_stop",
		"session_id":      "parent-session",
		"extra": map[string]interface{}{
			"child_role":   "researcher",
			"child_status": "completed",
			"duration_ms":  float64(42),
		},
	})

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "subagent.stopped" {
		t.Fatalf("event.action = %q, want subagent.stopped", action)
	}
	raw := event["raw"].(map[string]interface{})["subagent"].(map[string]interface{})
	if raw["role"] != "researcher" || raw["status"] != "completed" {
		t.Fatalf("subagent raw = %#v, want role/status metadata", raw)
	}
}
