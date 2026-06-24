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

		// Antigravity tools
		{"antigravity write", "antigravity", "write_file", true},
		{"antigravity patch", "antigravity", "apply_patch", true},
		{"antigravity command (not edit)", "antigravity", "run_command", false},
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

func TestRunPostToolEmitsAntigravityToolFailed(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{"/repo"},
		"toolCall": map[string]interface{}{
			"name": "run_command",
			"args": map[string]interface{}{
				"CommandLine": "npm test",
				"Cwd":         "/repo",
			},
		},
		"error": "exit status 1",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", action)
	}
	if severity := event["severity"]; severity != "high" {
		t.Fatalf("severity = %q, want high", severity)
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "npm test" {
		t.Fatalf("command = %q, want npm test", command)
	}
}

func TestRunPostToolEmitsCursorAfterShellExecution(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-shell",
		"hook_event_name": "afterShellExecution",
		"command":         "go test ./...",
		"output":          "ok",
		"duration":        float64(1234),
		"cwd":             "/repo",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", action)
	}
	command := event["command"].(map[string]interface{})
	if command["command"] != "go test ./..." || command["output"] != "ok" {
		t.Fatalf("command = %#v, want command and output", command)
	}
}

func TestRunPostToolCorrectsCursorPayloadWithDefaultPlatform(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "afterShellExecution",
		"session_id":      "conv-shell",
		"command":         "go test ./...",
		"output":          "ok",
		"cwd":             "/repo",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "cursor" {
		t.Fatalf("harness = %q, want cursor", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", action)
	}
	if sessionID := event["session"].(map[string]interface{})["id"]; sessionID != "conv-shell" {
		t.Fatalf("session.id = %q, want conv-shell", sessionID)
	}
}

func TestRunPostToolEmitsCursorMCPGenAIFields(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-mcp",
		"hook_event_name": "postToolUse",
		"tool_name":       "MCP:get_organizations",
		"tool_input": map[string]interface{}{
			"jsonrpc_request_id":   "req-1",
			"mcp_protocol_version": "2025-06-18",
			"mcp_session_id":       "mcp-session",
			"network_transport":    "pipe",
		},
		"tool_response": map[string]interface{}{"organizations": []interface{}{"meulo"}},
		"cwd":           "/repo",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "mcp.tool_invoked" {
		t.Fatalf("event.action = %q, want mcp.tool_invoked", action)
	}
	if tool := event["tool"].(map[string]interface{})["name"]; tool != "MCP:get_organizations" {
		t.Fatalf("tool.name = %q, want raw Cursor MCP tool name", tool)
	}
	mcp := event["mcp"].(map[string]interface{})
	if mcp["tool"] != "get_organizations" {
		t.Fatalf("mcp = %#v, want get_organizations", mcp)
	}
	if method := mcp["method"].(map[string]interface{})["name"]; method != "tools/call" {
		t.Fatalf("mcp.method.name = %q, want tools/call", method)
	}
	genAI := event["gen_ai"].(map[string]interface{})
	if operation := genAI["operation"].(map[string]interface{})["name"]; operation != "execute_tool" {
		t.Fatalf("gen_ai.operation.name = %q, want execute_tool", operation)
	}
	genAITool := genAI["tool"].(map[string]interface{})
	if genAITool["name"] != "get_organizations" {
		t.Fatalf("gen_ai.tool = %#v, want get_organizations", genAITool)
	}
	if result := genAITool["call"].(map[string]interface{})["result"]; result == nil {
		t.Fatalf("gen_ai.tool.call.result missing: %#v", genAITool)
	}
	if transport := event["network"].(map[string]interface{})["transport"]; transport != "pipe" {
		t.Fatalf("network.transport = %q, want pipe", transport)
	}
	if requestID := event["jsonrpc"].(map[string]interface{})["request"].(map[string]interface{})["id"]; requestID != "req-1" {
		t.Fatalf("jsonrpc.request.id = %q, want req-1", requestID)
	}
}

func TestRunPostToolSuppressesCursorCloudReadPostToolDuplicate(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "cursor_cloud")

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-read",
		"hook_event_name": "postToolUse",
		"tool_name":       "Read",
		"tool_input": map[string]interface{}{
			"path": "/repo/README.md",
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}
	assertNoEndpointLog(t, logPath)
}

func TestRunPostToolSeedsCursorCloudRunIDForAfterFileEdit(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "cursor_cloud")
	t.Setenv("BEACON_RUN_ID", "")

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-edit",
		"hook_event_name": "afterFileEdit",
		"file_path":       "/repo/main.go",
		"edits": []interface{}{
			map[string]interface{}{
				"old_string": "package main\n",
				"new_string": "package main\n\nfunc main() {}\n",
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	if runID := event["run"].(map[string]interface{})["run_id"]; runID != "conv-edit" {
		t.Fatalf("run.run_id = %q, want conv-edit", runID)
	}
}

func TestRunPostToolEmitsCursorPostToolFailure(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-failure",
		"hook_event_name": "postToolUseFailure",
		"tool_name":       "Shell",
		"tool_input": map[string]interface{}{
			"command": "npm test",
		},
		"error_message": "Command timed out",
		"failure_type":  "timeout",
		"cwd":           "/repo",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", action)
	}
	if severity := event["severity"]; severity != "high" {
		t.Fatalf("severity = %q, want high", severity)
	}
	tool := event["tool"].(map[string]interface{})
	if tool["name"] != "Shell" || tool["failure_type"] != "timeout" {
		t.Fatalf("tool = %#v, want Shell timeout", tool)
	}
}

func TestRunPostToolEmitsCursorMCPFailureResponseFields(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversation_id": "conv-mcp-failure",
		"hook_event_name": "postToolUseFailure",
		"tool_name":       "MCP:get_organizations",
		"tool_input": map[string]interface{}{
			"jsonrpc_request_id": "req-1",
		},
		"tool_response": map[string]interface{}{
			"error":      true,
			"mcp_server": "linear",
			"mcp_tool":   "get_organizations",
		},
		"error_message": "MCP tool failed",
		"failure_type":  "tool_error",
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", action)
	}
	errorFields := event["error"].(map[string]interface{})
	if got := errorFields["type"]; got != "tool_error" {
		t.Fatalf("error.type = %q, want tool_error", got)
	}
	mcp := event["mcp"].(map[string]interface{})
	if mcp["server"] != "linear" || mcp["tool"] != "get_organizations" {
		t.Fatalf("mcp = %#v, want linear get_organizations", mcp)
	}
	genAI := event["gen_ai"].(map[string]interface{})
	tool := genAI["tool"].(map[string]interface{})
	call := tool["call"].(map[string]interface{})
	if call["result"] == nil {
		t.Fatalf("gen_ai.tool.call = %#v, want result", call)
	}
}

func TestRunPostToolEmitsAntigravityFileModifiedEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversationId": "ag-session",
		"toolCall": map[string]interface{}{
			"name": "write_file",
			"args": map[string]interface{}{
				"Path":    "/repo/main.go",
				"content": "package main\n// token=ag-secret",
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/repo/main.go" {
		t.Fatalf("file = %#v, want /repo/main.go", file)
	}
	if _, ok := file["diff_hash"]; !ok {
		t.Fatalf("file diff_hash missing: %#v", file)
	}
}

func TestRunPostToolEmitsAntigravityFileReadPathFields(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"conversationId": "ag-session",
		"workspacePaths": []interface{}{"/repo"},
		"toolCall": map[string]interface{}{
			"name": "list_dir",
			"args": map[string]interface{}{
				"DirectoryPath": "/repo/docs",
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.read" {
		t.Fatalf("event.action = %q, want file.read", action)
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/repo/docs" || file["operation"] != "read" {
		t.Fatalf("file = %#v, want /repo/docs read", file)
	}
	tool := event["tool"].(map[string]interface{})
	if tool["path"] != "/repo/docs" {
		t.Fatalf("tool = %#v, want path /repo/docs", tool)
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

func TestRunPostToolEmitsDevinDesktopFileModifiedEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin-desktop"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPostTool, map[string]interface{}{
		"agent_action_name": "post_write_code",
		"trajectory_id":     "cascade-session",
		"execution_id":      "cascade-turn",
		"tool_info": map[string]interface{}{
			"file_path":      "/repo/main.go",
			"workspace_path": "/repo",
			"edits": []interface{}{
				map[string]interface{}{
					"old_string": "old",
					"new_string": "new token=devin-secret",
				},
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("post-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "devin-desktop" {
		t.Fatalf("harness = %q, want devin-desktop", harness)
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "cascade-session" || session["execution_id"] != "cascade-turn" {
		t.Fatalf("session = %#v, want Cascade trajectory and execution IDs", session)
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/repo/main.go" {
		t.Fatalf("file = %#v, want /repo/main.go", file)
	}
	if _, ok := file["diff_hash"]; !ok {
		t.Fatalf("file diff_hash missing: %#v", file)
	}
	if _, ok := event["raw"].(map[string]interface{})["cascade"]; !ok {
		t.Fatalf("raw.cascade missing: %#v", event["raw"])
	}
}

func TestRunPostToolEmitsDevinDesktopCascadeObservedEvents(t *testing.T) {
	tests := []struct {
		name       string
		actionName string
		toolInfo   map[string]interface{}
		wantAction string
		wantField  string
	}{
		{
			name:       "command",
			actionName: "post_run_command",
			toolInfo:   map[string]interface{}{"command": "go test ./...", "workspace_path": "/repo"},
			wantAction: "command.executed",
			wantField:  "command",
		},
		{
			name:       "mcp",
			actionName: "post_mcp_tool_use",
			toolInfo:   map[string]interface{}{"server_name": "linear", "tool_name": "issue-get", "workspace_path": "/repo"},
			wantAction: "mcp.tool_invoked",
			wantField:  "mcp",
		},
		{
			name:       "read",
			actionName: "post_read_code",
			toolInfo:   map[string]interface{}{"file_path": "/repo/main.go", "workspace_path": "/repo"},
			wantAction: "file.read",
			wantField:  "file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "devin-desktop"
			logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
			t.Setenv("BEACON_ENDPOINT_LOG", logPath)

			out := runHookWithInput(t, runPostTool, map[string]interface{}{
				"agent_action_name": tt.actionName,
				"trajectory_id":     "cascade-session",
				"execution_id":      "cascade-turn",
				"tool_info":         tt.toolInfo,
			})
			if len(out) != 0 {
				t.Fatalf("post-tool response = %#v, want empty response", out)
			}

			event := lastEndpointEvent(t, logPath)
			if action := event["event"].(map[string]interface{})["action"]; action != tt.wantAction {
				t.Fatalf("event.action = %q, want %q", action, tt.wantAction)
			}
			if _, ok := event[tt.wantField]; !ok {
				t.Fatalf("event missing %q field: %#v", tt.wantField, event)
			}
			tool := event["tool"].(map[string]interface{})
			if tool["execution_id"] != "cascade-turn" || tool["action"] != tt.actionName {
				t.Fatalf("tool = %#v, want Cascade action and execution IDs", tool)
			}
		})
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

func TestRunPostToolEmitsClaudeToolEvents(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	inputs := []map[string]interface{}{
		{
			"session_id":      "claude-session",
			"cwd":             "/repo",
			"hook_event_name": "PostToolUse",
			"tool_name":       "Edit",
			"tool_input": map[string]interface{}{
				"file_path":  "/repo/main.go",
				"old_string": "old",
				"new_string": "new token=claude-secret",
			},
			"tool_response": map[string]interface{}{"success": true},
		},
		{
			"session_id":      "claude-session",
			"cwd":             "/repo",
			"hook_event_name": "PostToolUse",
			"tool_name":       "Bash",
			"tool_input":      map[string]interface{}{"command": "go test ./..."},
			"tool_response":   map[string]interface{}{"stdout": "ok"},
		},
		{
			"session_id":      "claude-session",
			"cwd":             "/repo",
			"hook_event_name": "PostToolUseFailure",
			"tool_name":       "Bash",
			"tool_input":      map[string]interface{}{"command": "go test ./..."},
			"error":           "exit status 1",
		},
		{
			"session_id":      "claude-session",
			"cwd":             "/repo",
			"hook_event_name": "PostToolUse",
			"tool_name":       "mcp__memory__write",
			"tool_input":      map[string]interface{}{"name": "remember"},
			"tool_response":   map[string]interface{}{"ok": true},
		},
	}

	for _, input := range inputs {
		if out := runHookWithInput(t, runPostTool, input); len(out) != 0 {
			t.Fatalf("post-tool response = %#v, want empty response", out)
		}
	}

	events := endpointEvents(t, logPath)
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4: %#v", len(events), events)
	}
	wantActions := []string{"file.modified", "command.executed", "tool.failed", "mcp.tool_invoked"}
	for i, want := range wantActions {
		if got := events[i]["event"].(map[string]interface{})["action"]; got != want {
			t.Fatalf("event[%d].action = %q, want %q", i, got, want)
		}
		if harness := events[i]["harness"].(map[string]interface{})["name"]; harness != "claude" {
			t.Fatalf("event[%d] harness = %q, want claude", i, harness)
		}
	}
	if file := events[0]["file"].(map[string]interface{}); file["path"] != "/repo/main.go" || file["operation"] != "modify" {
		t.Fatalf("file event = %#v, want main.go modify", file)
	}
	if command := events[1]["command"].(map[string]interface{})["command"]; command != "go test ./..." {
		t.Fatalf("command = %q, want go test ./...", command)
	}
	if severity := events[2]["severity"]; severity != "high" {
		t.Fatalf("failure severity = %q, want high", severity)
	}
	mcp := events[3]["mcp"].(map[string]interface{})
	if mcp["server"] != "memory" || mcp["tool"] != "write" {
		t.Fatalf("mcp = %#v, want server memory tool write", mcp)
	}
	if method := mcp["method"].(map[string]interface{})["name"]; method != "tools/call" {
		t.Fatalf("mcp.method.name = %q, want tools/call", method)
	}
	genAI := events[3]["gen_ai"].(map[string]interface{})
	if operation := genAI["operation"].(map[string]interface{})["name"]; operation != "execute_tool" {
		t.Fatalf("gen_ai.operation.name = %q, want execute_tool", operation)
	}
	genAITool := genAI["tool"].(map[string]interface{})
	if genAITool["name"] != "write" {
		t.Fatalf("gen_ai.tool = %#v, want write", genAITool)
	}
	if result := genAITool["call"].(map[string]interface{})["result"]; result == nil {
		t.Fatalf("gen_ai.tool.call.result missing: %#v", genAITool)
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
