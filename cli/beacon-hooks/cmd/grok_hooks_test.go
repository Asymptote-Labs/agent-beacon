package cmd

import (
	"path/filepath"
	"strings"
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

func TestGrokPreToolRecordsToolWithoutApproving(t *testing.T) {
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
	if out == nil || len(out) != 0 {
		t.Fatalf("pre-tool response = %#v, want an empty object carrying no decision", out)
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

func TestGrokPostToolFailedEditsRemainFailed(t *testing.T) {
	for _, tt := range []struct {
		name, eventKey, eventName, toolName, extension, errorText string
	}{
		{"native write failure", "hookEventName", "post_tool_use_failure", "write_file", ".go", ""},
		{"camelCase event", "hook_event_name", "postToolUseFailure", "write_file", ".go", ""},
		{"explicit error", "hookEventName", "post_tool_use", "write_file", ".go", "permission denied"},
		{"unscannable path", "hookEventName", "post_tool_use_failure", "write_file", ".md", ""},
		{"edit failure", "hookEventName", "post_tool_use_failure", "Edit", ".go", ""},
		{"search replace failure", "hookEventName", "post_tool_use_failure", "search_replace", ".go", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "grok"
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			t.Setenv("BEACON_ENDPOINT_LOG", logPath)
			path := filepath.Join(t.TempDir(), "not-written"+tt.extension)
			input := map[string]interface{}{
				tt.eventKey:   tt.eventName,
				"sessionId":   "grok-failed-edit",
				"toolName":    tt.toolName,
				"tool_use_id": "failed-edit-call",
				"toolInput": map[string]interface{}{
					"path": path, "content": "package requested_but_not_written\n",
					"old_string": "package old", "new_string": "package requested_but_not_written",
				},
				"error": tt.errorText,
			}
			// A structured patch makes search_replace eligible for the success-only
			// diff path even though its name has no dedicated diff constructor.
			if tt.toolName == "search_replace" {
				input["toolResponse"] = map[string]interface{}{"structuredPatch": []interface{}{
					map[string]interface{}{
						"oldStart": 1, "oldLines": 1, "newStart": 1, "newLines": 1,
						"lines": []interface{}{"-package old", "+package requested_but_not_written"},
					},
				}}
			}
			out := runHookWithInput(t, runPostTool, input)
			if len(out) != 0 {
				t.Fatalf("response = %#v, want empty object", out)
			}
			events := endpointEvents(t, logPath)
			if len(events) != 1 {
				t.Fatalf("events = %#v, want exactly one failed attempt", events)
			}
			event := events[0]
			classification := event["event"].(map[string]interface{})
			if classification["action"] != "tool.failed" || classification["category"] != "tool" || event["severity"] != "high" {
				t.Fatalf("classification = %#v severity %v, want tool.failed/tool/high", classification, event["severity"])
			}
			if session := event["session"].(map[string]interface{}); session["id"] != "grok-failed-edit" {
				t.Fatalf("session = %#v, want attempted session", session)
			}
			if tool := event["tool"].(map[string]interface{}); tool["name"] != tt.toolName || tool["path"] != path {
				t.Fatalf("tool = %#v, want attempted tool and path", tool)
			}
			file := event["file"].(map[string]interface{})
			if file["path"] != path {
				t.Fatalf("file.path = %v, want %s", file["path"], path)
			}
			for _, key := range []string{"diff", "diff_hash", "diff_bytes"} {
				if _, present := file[key]; present {
					t.Fatalf("failed attempt has fabricated file.%s: %#v", key, file)
				}
			}
			genAI := event["gen_ai"].(map[string]interface{})
			call := genAI["tool"].(map[string]interface{})["call"].(map[string]interface{})
			if call["id"] != "failed-edit-call" {
				t.Fatalf("call.id = %v, want attempted call", call["id"])
			}
			raw := event["raw"].(map[string]interface{})["grok"].(map[string]interface{})
			if raw[tt.eventKey] != tt.eventName || raw["toolName"] != tt.toolName || raw["error"] != tt.errorText {
				t.Fatalf("raw.grok = %#v, want native failed-attempt payload", raw)
			}
		})
	}
}

func TestGrokPostToolSuccessfulWriteRetainsDiff(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "grok"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	path := filepath.Join(t.TempDir(), "written.go")
	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hookEventName": "post_tool_use",
		"sessionId":     "grok-successful-edit",
		"toolName":      "write_file",
		"toolInput":     map[string]interface{}{"path": path, "content": "package written\n"},
		"error":         "",
	})
	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one successful edit", events)
	}
	event := events[0]
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %v, want file.modified", action)
	}
	file := event["file"].(map[string]interface{})
	diff, _ := file["diff"].(string)
	hash, _ := file["diff_hash"].(string)
	bytes, _ := file["diff_bytes"].(float64)
	if file["path"] != path || !strings.Contains(diff, "+package written") || hash == "" || bytes <= 0 {
		t.Fatalf("file = %#v, want successful write path and retained diff metadata", file)
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
