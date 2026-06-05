package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

func TestRunPreToolAllowsAndEmitsTelemetry(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
	}{
		{
			name: "missing session allows",
		},
		{
			name:      "session allows",
			sessionID: "conv-observe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "cursor"

			input := map[string]interface{}{}
			if tt.sessionID != "" {
				input["conversation_id"] = tt.sessionID
			}
			out := runHookWithInput(t, runPreTool, input)

			if out["permission"] != "allow" {
				t.Fatalf("permission = %v, want allow; output=%#v", out["permission"], out)
			}
		})
	}
}

func TestRunPromptSubmitUsesCursorResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{"conversation_id": "conv-submit"})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}
}

func TestVSCodeHooksEmitLowNoiseTelemetry(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "vscode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"sessionId":     "vscode-session",
		"hookEventName": "PreToolUse",
		"cwd":           "/repo",
		"tool_name":     "runCommand",
		"tool_input": map[string]interface{}{
			"command": "go test ./...",
		},
	})
	if len(out) != 0 {
		t.Fatalf("vscode pre-tool output = %#v, want empty non-controlling response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "vscode" {
		t.Fatalf("harness = %q, want vscode", harness)
	}
	if _, ok := event["raw"].(map[string]interface{})["vscode"]; !ok {
		t.Fatalf("raw.vscode missing: %#v", event["raw"])
	}
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt: %#v", event["prompt"])
	}
}

func TestRunSubagentLifecycleEmitsVSCodeEvents(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "vscode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, func(cmd *cobra.Command, args []string) {
		runSubagentLifecycle("subagent.started", "Subagent started")
	}, map[string]interface{}{
		"sessionId":  "vscode-session",
		"agent_id":   "agent-1",
		"agent_type": "Plan",
	})

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "subagent.started" {
		t.Fatalf("event.action = %q, want subagent.started", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "vscode" {
		t.Fatalf("harness = %q, want vscode", harness)
	}
	if _, ok := event["tool"]; ok {
		t.Fatalf("subagent event should not be encoded as tool: %#v", event["tool"])
	}
}

func TestRunPromptSubmitEmitsFactoryPromptEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "factory"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "factory-session",
		"transcript_path": "/tmp/factory-session.jsonl",
		"cwd":             "/repo",
		"hook_event_name": "UserPromptSubmit",
		"permission_mode": "off",
		"prompt":          "summarize token=prompt-secret",
	})
	if len(out) != 0 {
		t.Fatalf("factory prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := event["message"]; got != "Prompt submitted to agent" {
		t.Fatalf("message = %q, want prompt submitted", got)
	}
	harness := event["harness"].(map[string]interface{})
	if harness["name"] != "factory" {
		t.Fatalf("harness = %#v, want factory", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	prompt := event["prompt"].(map[string]interface{})
	if got := prompt["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
}

func TestRunPromptSubmitEmitsAntigravityPromptEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{"/repo"},
		"prompt":         "summarize token=ag-secret",
	})
	if len(out) != 0 {
		t.Fatalf("antigravity prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "antigravity" {
		t.Fatalf("harness = %q, want antigravity", harness)
	}
	if got := event["prompt"].(map[string]interface{})["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if got := event["repository"]; got != "/repo" {
		t.Fatalf("repository = %q, want /repo", got)
	}
}

func TestRunPromptSubmitEmitsAntigravityUserPromptEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversationId": "ag-session",
		"userPrompt":     "explain this repo token=ag-secret",
	})
	if len(out) != 0 {
		t.Fatalf("antigravity prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if got := event["prompt"].(map[string]interface{})["text"]; got != "explain this repo token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
}

func TestRunPromptSubmitOmitsAntigravityPromptForMetadataRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversationId": "ag-session",
		"prompt":         "summarize this file",
	})
	if len(out) != 0 {
		t.Fatalf("antigravity prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt field: %#v", event["prompt"])
	}
}

func TestRunSessionLifecycleEmitsAntigravityEvents(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	input := map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{"/repo"},
	}
	runHookWithInput(t, runSessionStart, input)
	runHookWithInput(t, runSessionEnd, input)

	events := endpointEvents(t, logPath)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2: %#v", len(events), events)
	}
	want := []string{"session.started", "session.ended"}
	for i, action := range want {
		if got := events[i]["event"].(map[string]interface{})["action"]; got != action {
			t.Fatalf("event[%d].action = %q, want %q", i, got, action)
		}
		if session := events[i]["session"].(map[string]interface{}); session["id"] != "ag-session" {
			t.Fatalf("event[%d] session = %#v, want ag-session", i, session)
		}
	}
}

func TestRunPromptSubmitEmitsDevinPromptWithoutSession(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	t.Setenv("DEVIN_PROJECT_DIR", "/repo")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "summarize token=devin-secret",
	})
	if len(out) != 0 {
		t.Fatalf("devin prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if _, ok := event["session"].(map[string]interface{})["id"]; ok {
		t.Fatalf("devin event should not invent session id: %#v", event["session"])
	}
	if got := event["repository"]; got != "/repo" {
		t.Fatalf("repository = %q, want DEVIN_PROJECT_DIR", got)
	}
}

func TestRunPromptSubmitEmitsDevinDesktopPromptWithDesktopHarness(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin-desktop"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"agent_action_name": "pre_user_prompt",
		"trajectory_id":     "cascade-session",
		"execution_id":      "cascade-turn",
		"tool_info": map[string]interface{}{
			"user_prompt":    "summarize token=desktop-secret",
			"workspace_path": "/repo",
		},
	})
	if len(out) != 0 {
		t.Fatalf("devin desktop prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "devin-desktop" {
		t.Fatalf("harness = %q, want devin-desktop", harness)
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "cascade-session" || session["execution_id"] != "cascade-turn" {
		t.Fatalf("session = %#v, want Cascade trajectory and execution IDs", session)
	}
	if got := event["repository"]; got != "/repo" {
		t.Fatalf("repository = %q, want Cascade workspace path", got)
	}
}

func TestRunPromptSubmitOmitsDevinDesktopPromptForMetadataRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin-desktop"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"agent_action_name": "pre_user_prompt",
		"trajectory_id":     "cascade-session",
		"tool_info": map[string]interface{}{
			"user_prompt": "summarize token=desktop-secret",
		},
	})
	if len(out) != 0 {
		t.Fatalf("devin desktop prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt: %#v", event["prompt"])
	}
	content := event["content"].(map[string]interface{})
	if content["included"] != false {
		t.Fatalf("content = %#v, want included=false", content)
	}
}

func TestRunPreToolEmitsDevinToolTelemetryWithoutApproval(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"hook_event_name": "PreToolUse",
		"tool_name":       "exec",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if len(out) != 0 {
		t.Fatalf("devin pre-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("PreToolUse should not emit approval telemetry: %#v", event["approval"])
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "git status" {
		t.Fatalf("command = %q, want git status", command)
	}
}

func TestRunPermissionRequestApprovesDevinCLI(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin-cli"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"tool_name": "exec",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if out["decision"] != "approve" {
		t.Fatalf("devin-cli permission response = %#v, want approve", out)
	}

	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "devin-cli" {
		t.Fatalf("harness = %q, want devin-cli", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.allowed" {
		t.Fatalf("event.action = %q, want approval.allowed", action)
	}
}

func TestRunPreToolEmitsClaudeTelemetryWithoutDecision(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"session_id":      "claude-session",
		"transcript_path": "/tmp/claude-session.jsonl",
		"cwd":             "/repo",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input": map[string]interface{}{
			"command": "go test ./...",
		},
	})
	if len(out) != 0 {
		t.Fatalf("claude pre-tool response = %#v, want empty non-controlling response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "claude" {
		t.Fatalf("harness = %q, want claude", harness)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("Claude PreToolUse should not emit approval telemetry: %#v", event["approval"])
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "go test ./..." {
		t.Fatalf("command = %q, want go test ./...", command)
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "claude-session" || session["working_directory"] != "/repo" {
		t.Fatalf("session = %#v, want id and working_directory", session)
	}
}

func TestRunPreToolEmitsAntigravityCommandTelemetry(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{
			"/repo",
		},
		"toolCall": map[string]interface{}{
			"name": "run_command",
			"args": map[string]interface{}{
				"CommandLine": "npm test",
				"Cwd":         "/repo",
			},
		},
		"stepIdx": 19,
	})
	if out["decision"] != "allow" {
		t.Fatalf("antigravity pre-tool response = %#v, want decision=allow", out)
	}

	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "antigravity" {
		t.Fatalf("harness = %q, want antigravity", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("Antigravity PreToolUse should not emit approval telemetry: %#v", event["approval"])
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "npm test" {
		t.Fatalf("command = %q, want npm test", command)
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "ag-session" || session["working_directory"] != "/repo" {
		t.Fatalf("session = %#v, want id and working_directory", session)
	}
}

func TestRunPreToolSynthesizesAntigravityPromptFromTranscript(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\ntell me about token=ag-secret\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nignored\n</ADDITIONAL_METADATA>"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	input := map[string]interface{}{
		"conversationId":  "ag-session",
		"transcriptPath":  transcriptPath,
		"workspacePaths":  []interface{}{"/repo"},
		"toolCall":        map[string]interface{}{"name": "list_dir", "args": map[string]interface{}{"DirectoryPath": `"/repo"`}},
		"artifactDirPath": "/tmp/artifacts",
	}
	out := runHookWithInput(t, runPreTool, input)
	if out["decision"] != "allow" {
		t.Fatalf("antigravity pre-tool response = %#v, want decision=allow", out)
	}
	out = runHookWithInput(t, runPreTool, input)
	if out["decision"] != "allow" {
		t.Fatalf("second antigravity pre-tool response = %#v, want decision=allow", out)
	}

	events := endpointEvents(t, logPath)
	if len(events) != 3 {
		t.Fatalf("event count = %d, want prompt plus two tool events: %#v", len(events), events)
	}
	if action := events[0]["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("first event.action = %q, want prompt.submitted", action)
	}
	if got := events[0]["prompt"].(map[string]interface{})["text"]; got != "tell me about token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted transcript prompt", got)
	}
	if got := events[1]["file"].(map[string]interface{})["path"]; got != "/repo" {
		t.Fatalf("file.path = %q, want normalized /repo", got)
	}
}

func TestRunPreToolRetriesAntigravityPromptUntilTranscriptExists(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	input := map[string]interface{}{
		"conversationId": "ag-session",
		"transcriptPath": transcriptPath,
		"workspacePaths": []interface{}{"/repo"},
		"toolCall":       map[string]interface{}{"name": "list_dir", "args": map[string]interface{}{"DirectoryPath": `"/repo"`}},
	}

	if out := runHookWithInput(t, runPreTool, input); out["decision"] != "allow" {
		t.Fatalf("first antigravity pre-tool response = %#v, want decision=allow", out)
	}
	if err := os.WriteFile(transcriptPath, []byte(`{"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nshow me the full prompt\n</USER_REQUEST>"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := runHookWithInput(t, runPreTool, input); out["decision"] != "allow" {
		t.Fatalf("second antigravity pre-tool response = %#v, want decision=allow", out)
	}

	events := endpointEvents(t, logPath)
	if len(events) != 3 {
		t.Fatalf("event count = %d, want first tool, prompt, second tool: %#v", len(events), events)
	}
	if action := events[1]["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("second event.action = %q, want prompt.submitted", action)
	}
	if got := events[1]["prompt"].(map[string]interface{})["text"]; got != "show me the full prompt" {
		t.Fatalf("prompt.text = %q, want full transcript prompt", got)
	}
}

func TestRunPermissionRequestEmitsDevinApproval(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "PermissionRequest",
		"tool_name":       "exec",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if out["decision"] != "approve" {
		t.Fatalf("devin permission response = %#v, want decision=approve", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.allowed" {
		t.Fatalf("event.action = %q, want approval.allowed", action)
	}
	approval := event["approval"].(map[string]interface{})
	if approval["decision"] != "approve" {
		t.Fatalf("approval = %#v, want approve", approval)
	}
}

func TestRunPermissionRequestEmitsClaudeTelemetryWithoutDecision(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"session_id":      "claude-session",
		"cwd":             "/repo",
		"hook_event_name": "PermissionRequest",
		"tool_name":       "Bash",
		"tool_input":      map[string]interface{}{"command": "git status"},
	})
	if len(out) != 0 {
		t.Fatalf("claude permission response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.requested" {
		t.Fatalf("event.action = %q, want approval.requested", action)
	}
	approval := event["approval"].(map[string]interface{})
	if approval["decision"] != "requested" {
		t.Fatalf("approval = %#v, want requested", approval)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "git status" {
		t.Fatalf("command = %q, want git status", command)
	}
}

func TestRunSessionLifecycleEmitsDevinEventsWithoutSession(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("DEVIN_PROJECT_DIR", "/repo")

	runHookWithInput(t, runSessionStart, map[string]interface{}{"source": "startup"})
	runHookWithInput(t, runStop, map[string]interface{}{"stop_hook_active": false})
	runHookWithInput(t, runSessionEnd, map[string]interface{}{"reason": "exit"})

	events := endpointEvents(t, logPath)
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3: %#v", len(events), events)
	}
	want := []string{"session.started", "tool.completed", "session.ended"}
	for i, action := range want {
		if got := events[i]["event"].(map[string]interface{})["action"]; got != action {
			t.Fatalf("event[%d].action = %q, want %q", i, got, action)
		}
		if session, ok := events[i]["session"].(map[string]interface{}); ok {
			if _, hasID := session["id"]; hasID {
				t.Fatalf("event[%d] should not invent session id: %#v", i, session)
			}
		}
	}
}

func TestRunPromptSubmitEmitsTypedPromptForFullRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ENDPOINT_CONFIG", filepath.Join(t.TempDir(), "missing-config.json"))

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-submit",
		"prompt":          "summarize token=prompt-secret",
	})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}

	event := lastEndpointEvent(t, logPath)
	prompt, ok := event["prompt"].(map[string]interface{})
	if !ok {
		t.Fatalf("prompt field missing: %#v", event)
	}
	if got := prompt["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if _, ok := event["raw"]; ok {
		t.Fatalf("raw should not carry prompt payload: %#v", event["raw"])
	}
	if events := endpointEvents(t, logPath); len(events) != 1 {
		t.Fatalf("endpoint event count = %d, want 1; events=%#v", len(events), events)
	}
}

func TestRunPromptSubmitOmitsPromptForMetadataRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-submit",
		"prompt":          "summarize this file",
	})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt field: %#v", event["prompt"])
	}
}

func TestRunSessionStartStoresModel(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runSessionStart, map[string]interface{}{
		"conversation_id": "conv-model",
		"model":           "gpt-5.5",
	})
	if len(out) != 0 {
		t.Fatalf("session-start response = %#v, want empty response", out)
	}
	if got := state.NewSessionState("conv-model", "cursor").GetModel(); got != "gpt-5.5" {
		t.Fatalf("stored model = %q, want gpt-5.5", got)
	}
}

func TestRunSessionEndRemovesSessionLog(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logFile := hookconfig.GetSessionLogFile("cursor", "conv-end")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		t.Fatalf("mkdir session log dir: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("session log"), 0644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	out := runHookWithInput(t, runSessionEnd, map[string]interface{}{"conversation_id": "conv-end"})
	if len(out) != 0 {
		t.Fatalf("session-end response = %#v, want empty response", out)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("session log still exists or unexpected error: %v", err)
	}
}

func setupHookConfigDirs(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	origBeaconDir := hookconfig.BeaconDir
	origClaudeDir := hookconfig.ClaudeDir
	origAntigravityDir := hookconfig.AntigravityDir
	origCopilotDir := hookconfig.CopilotDir
	origCursorDir := hookconfig.CursorDir
	origVSCodeDir := hookconfig.VSCodeDir
	origDevinDir := hookconfig.DevinDir
	origFactoryDir := hookconfig.FactoryDir
	origGrokDir := hookconfig.GrokDir
	origHermesDir := hookconfig.HermesDir
	origOpenCodeDir := hookconfig.OpenCodeDir
	origPlatform := platformFlag
	hookconfig.BeaconDir = tmp
	hookconfig.ClaudeDir = filepath.Join(tmp, "claude")
	hookconfig.AntigravityDir = filepath.Join(tmp, "antigravity")
	hookconfig.CopilotDir = filepath.Join(tmp, "copilot")
	hookconfig.CursorDir = filepath.Join(tmp, "cursor")
	hookconfig.VSCodeDir = filepath.Join(tmp, "vscode")
	hookconfig.DevinDir = filepath.Join(tmp, "devin")
	hookconfig.FactoryDir = filepath.Join(tmp, "factory")
	hookconfig.GrokDir = filepath.Join(tmp, "grok")
	hookconfig.HermesDir = filepath.Join(tmp, "hermes")
	hookconfig.OpenCodeDir = filepath.Join(tmp, "opencode")
	t.Cleanup(func() {
		hookconfig.BeaconDir = origBeaconDir
		hookconfig.ClaudeDir = origClaudeDir
		hookconfig.AntigravityDir = origAntigravityDir
		hookconfig.CopilotDir = origCopilotDir
		hookconfig.CursorDir = origCursorDir
		hookconfig.VSCodeDir = origVSCodeDir
		hookconfig.DevinDir = origDevinDir
		hookconfig.FactoryDir = origFactoryDir
		hookconfig.GrokDir = origGrokDir
		hookconfig.HermesDir = origHermesDir
		hookconfig.OpenCodeDir = origOpenCodeDir
		platformFlag = origPlatform
	})
}

func runHookWithInput(t *testing.T, run func(cmd *cobra.Command, args []string), input map[string]interface{}) map[string]interface{} {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = stdinR
	os.Stdout = stdoutW
	defer func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = stdinR.Close()
		_ = stdoutR.Close()
	}()

	if err := json.NewEncoder(stdinW).Encode(input); err != nil {
		t.Fatalf("encode input: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	run(nil, nil)

	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdin = origStdin
	os.Stdout = origStdout
	var out map[string]interface{}
	if err := json.NewDecoder(stdoutR).Decode(&out); err != nil {
		t.Fatalf("decode hook output: %v", err)
	}
	return out
}

func lastEndpointEvent(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	events := endpointEvents(t, path)
	if len(events) == 0 {
		t.Fatal("endpoint log was empty")
	}
	return events[len(events)-1]
}

func endpointEvents(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatal("endpoint log was empty")
	}
	events := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode endpoint event: %v", err)
		}
		events = append(events, event)
	}
	return events
}
