package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestOpenCodeEventRecordsPrompt(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "chat.message",
		"session_id": "session-1",
		"directory":  "/tmp/project",
		"model":      "anthropic/claude-sonnet-4",
		"output": map[string]interface{}{
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "text": "summarize token=opencode-secret"},
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "opencode" {
		t.Fatalf("harness.name = %q, want opencode", harness)
	}
	if got := event["prompt"].(map[string]interface{})["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if got := event["model"]; got != "anthropic/claude-sonnet-4" {
		t.Fatalf("model = %q, want opencode model", got)
	}
}

func TestOpenCodeEventFixtureRecordsPrompt(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	input := readOpenCodeFixture(t, "chat_message.json")
	runHookWithInput(t, runOpenCodeEvent, input)
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("fixture event.action = %q, want prompt.submitted", action)
	}
}

func TestOpenCodeFixtureContracts(t *testing.T) {
	tests := []struct {
		file     string
		action   string
		category string
	}{
		{file: "tool_execute_after_bash.json", action: "command.executed", category: "command"},
		{file: "tool_part_error.json", action: "tool.failed", category: "tool"},
		{file: "assistant_completed.json", action: "agent.response.completed", category: "session"},
		{file: "session_diff.json", action: "file.modified", category: "file"},
		{file: "permission_replied.json", action: "approval.denied", category: "approval"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			input := readOpenCodeFixture(t, tt.file)
			sessionID := resolveSessionID(input, "opencode")
			action, category, _, _, fields := opencodeEndpointEvent(input, sessionID)
			if action != tt.action || category != tt.category {
				t.Fatalf("event = %s/%s, want %s/%s", action, category, tt.action, tt.category)
			}
			if fields["raw"] == nil {
				t.Fatal("raw opencode payload missing")
			}
		})
	}
}

func TestOpenCodeEventAppliesContentMetadataDespiteLegacyRetentionEnv(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "chat.message",
		"session_id": "session-1",
		"output": map[string]interface{}{
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "text": "secret prompt"},
			},
		},
	})
	event := lastEndpointEvent(t, logPath)
	if got := event["prompt"].(map[string]interface{})["text"]; got != "secret prompt" {
		t.Fatalf("prompt.text = %q, want legacy retention env ignored", got)
	}
	raw := event["raw"].(map[string]interface{})
	if _, ok := raw["opencode"]; !ok {
		t.Fatalf("legacy retention env should not omit raw opencode payload: %#v", raw)
	}
	content := event["content"].(map[string]interface{})
	if content["retention"] != "full" || content["included"] != true {
		t.Fatalf("content metadata = %#v, want full/included", content)
	}
}

func TestOpenCodeEventMapsPermissionDecision(t *testing.T) {
	fields := map[string]interface{}{
		"type":       "permission.replied",
		"session_id": "session-1",
		"tool":       "bash",
		"decision":   "accepted",
	}
	action, category, _, _, eventFields := opencodeEndpointEvent(fields, "session-1")
	if category != "approval" {
		t.Fatalf("category = %q, want approval", category)
	}
	if action != "approval.allowed" {
		t.Fatalf("action = %q, want approval.allowed", action)
	}
	approval := eventFields["approval"].(map[string]interface{})
	if approval["decision"] != "accepted" {
		t.Fatalf("approval.decision = %q, want accepted", approval["decision"])
	}
}

func TestOpenCodeEventIgnoresUnsupportedEvents(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "installation.updated",
		"session_id": "session-1",
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
	assertNoEndpointLog(t, logPath)

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "tool.completed",
		"session_id": "session-1",
	})
	assertNoEndpointLog(t, logPath)

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "session.compacted",
		"session_id": "session-1",
	})
	assertNoEndpointLog(t, logPath)
}

func TestOpenCodeToolLifecycleNormalization(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		input    map[string]interface{}
		response map[string]interface{}
		action   string
		category string
	}{
		{
			name: "bash", tool: "bash",
			input:    map[string]interface{}{"command": "git status --short"},
			response: map[string]interface{}{"output": " M README.md", "metadata": map[string]interface{}{"exitCode": 0}},
			action:   "command.executed", category: "command",
		},
		{
			name: "read", tool: "read",
			input:    map[string]interface{}{"filePath": "/repo/README.md"},
			response: map[string]interface{}{"output": "Beacon"},
			action:   "file.read", category: "file",
		},
		{
			name: "write", tool: "write",
			input:    map[string]interface{}{"filePath": "/repo/test.txt", "content": "token=fixture-secret"},
			response: map[string]interface{}{"output": "ok"},
			action:   "file.modified", category: "file",
		},
		{
			name: "webfetch", tool: "webfetch",
			input:    map[string]interface{}{"url": "https://example.com"},
			response: map[string]interface{}{"output": "Example Domain"},
			action:   "tool.completed", category: "tool",
		},
		{
			name: "mcp", tool: "mcp__github__get_issue",
			input:    map[string]interface{}{"owner": "asymptote-labs", "repo": "agent-beacon"},
			response: map[string]interface{}{"output": "issue"},
			action:   "mcp.tool_invoked", category: "mcp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := map[string]interface{}{
				"type":          "tool.execute.after",
				"session_id":    "ses_test",
				"directory":     "/repo",
				"tool_name":     tt.tool,
				"call_id":       "call_" + tt.name,
				"duration_ms":   12,
				"tool_input":    tt.input,
				"tool_response": tt.response,
			}
			action, category, _, _, fields := opencodeEndpointEvent(input, "ses_test")
			if action != tt.action || category != tt.category {
				t.Fatalf("event = %s/%s, want %s/%s", action, category, tt.action, tt.category)
			}
			genAI := fields["gen_ai"].(map[string]interface{})
			tool := genAI["tool"].(map[string]interface{})
			call := tool["call"].(map[string]interface{})
			if call["id"] != "call_"+tt.name {
				t.Fatalf("call id = %#v", call["id"])
			}
			if tt.category == "file" {
				file := fields["file"].(map[string]interface{})
				if file["path"] == "" {
					t.Fatalf("file path missing: %#v", file)
				}
			}
		})
	}
}

func TestOpenCodeCompletedAssistantNormalizesUsageOnce(t *testing.T) {
	input := map[string]interface{}{
		"type":       "message.updated",
		"session_id": "ses_test",
		"model":      "moonshotai/kimi-k3",
		"properties": map[string]interface{}{
			"info": map[string]interface{}{
				"id": "msg_1", "role": "assistant", "providerID": "moonshotai", "modelID": "kimi-k3",
				"finish": "stop", "cost": 0.42,
				"tokens": map[string]interface{}{
					"input": 10, "output": 5, "reasoning": 2,
					"cache": map[string]interface{}{"read": 7, "write": 1},
				},
			},
		},
	}
	action, _, _, _, fields := opencodeEndpointEvent(input, "ses_test")
	if action != "agent.response.completed" {
		t.Fatalf("action = %q", action)
	}
	usage := fields["gen_ai"].(map[string]interface{})["usage"].(map[string]interface{})
	if usage["input_tokens"] != 10 || usage["output_tokens"] != 5 || usage["cost_usd"] != 0.42 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestOpenCodePartNormalization(t *testing.T) {
	for _, tt := range []struct {
		partType string
		action   string
	}{
		{partType: "text", action: "agent.response"},
		{partType: "reasoning", action: "agent.reasoning"},
	} {
		t.Run(tt.partType, func(t *testing.T) {
			action, _, _, _, fields := opencodeEndpointEvent(map[string]interface{}{
				"type":       "message.part.updated",
				"session_id": "ses_test",
				"part": map[string]interface{}{
					"type": tt.partType, "text": "token=part-secret",
				},
			}, "ses_test")
			if action != tt.action {
				t.Fatalf("action = %q, want %q", action, tt.action)
			}
			if fields["content"] == nil || fields["gen_ai"] == nil {
				t.Fatalf("missing content/gen_ai: %#v", fields)
			}
		})
	}
}

func TestOpenCodeStructuredDiffSuppressesEmptyAndEmitsChangedFiles(t *testing.T) {
	base := map[string]interface{}{
		"type":       "session.diff",
		"session_id": "ses_test",
		"properties": map[string]interface{}{"diff": []interface{}{}},
	}
	if events := opencodeEndpointEvents(base, "ses_test"); len(events) != 0 {
		t.Fatalf("empty diff emitted events: %#v", events)
	}

	base["properties"] = map[string]interface{}{"diff": []interface{}{
		map[string]interface{}{"file": "/repo/a.go", "before": "old", "after": "new", "additions": 1, "deletions": 1},
		map[string]interface{}{"file": "/repo/unchanged.go", "before": "same", "after": "same", "additions": 0, "deletions": 0},
	}}
	events := opencodeEndpointEvents(base, "ses_test")
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	file := events[0].fields["file"].(map[string]interface{})
	if file["path"] != "/repo/a.go" || file["diff_hash"] == "" {
		t.Fatalf("file = %#v", file)
	}
}

func TestOpenCodeIdleIsStatusUntilSessionDeletion(t *testing.T) {
	action, _, _, _, _ := opencodeEndpointEvent(map[string]interface{}{
		"type":       "session.status",
		"session_id": "ses_test",
		"properties": map[string]interface{}{"status": map[string]interface{}{"type": "idle"}},
	}, "ses_test")
	if action != "session.status" {
		t.Fatalf("idle action = %q, want session.status", action)
	}
	action, _, _, _, _ = opencodeEndpointEvent(map[string]interface{}{
		"type":       "session.deleted",
		"session_id": "ses_test",
	}, "ses_test")
	if action != "session.ended" {
		t.Fatalf("deleted action = %q, want session.ended", action)
	}
}

func TestOpenCodeBashCapturesExitMetadata(t *testing.T) {
	_, _, _, _, fields := opencodeEndpointEvent(map[string]interface{}{
		"type":       "tool.execute.after",
		"session_id": "ses_test",
		"tool_name":  "bash",
		"call_id":    "bash_1",
		"tool_input": map[string]interface{}{"command": "test -f missing"},
		"tool_response": map[string]interface{}{
			"output": "missing",
			"metadata": map[string]interface{}{
				"exit": 1,
			},
		},
	}, "ses_test")
	command := fields["command"].(map[string]interface{})
	if command["exit_code"] != 1 {
		t.Fatalf("exit_code = %#v, want 1", command["exit_code"])
	}
}

func TestOpenCodeWatcherUnlinkNormalizesFileDeletion(t *testing.T) {
	action, category, _, _, fields := opencodeEndpointEvent(map[string]interface{}{
		"type":       "file.watcher.updated",
		"session_id": "ses_test",
		"properties": map[string]interface{}{
			"sessionID": "ses_test",
			"file":      "/repo/.tmp/e2e.txt",
			"event":     "unlink",
			"callID":    "call_rm",
		},
	}, "ses_test")
	if action != "file.modified" || category != "file" {
		t.Fatalf("event = %s/%s", action, category)
	}
	file := fields["file"].(map[string]interface{})
	if file["operation"] != "delete" || file["path"] != "/repo/.tmp/e2e.txt" {
		t.Fatalf("file = %#v", file)
	}
	call := fields["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	if call["id"] != "call_rm" {
		t.Fatalf("call = %#v", call)
	}
}

func TestOpenCodeSuccessfulRMEmitsCorrelatedFileDeletion(t *testing.T) {
	input := map[string]interface{}{
		"type":       "tool.execute.after",
		"session_id": "ses_test",
		"tool_name":  "bash",
		"call_id":    "call_rm",
		"tool_input": map[string]interface{}{"command": `rm "/repo/.tmp/e2e.txt"`},
		"tool_response": map[string]interface{}{
			"output":   "",
			"metadata": map[string]interface{}{"exit": 0},
		},
		"file_mutations": []interface{}{
			map[string]interface{}{"path": "/repo/.tmp/e2e.txt", "operation": "delete"},
		},
	}
	events := opencodeEndpointEvents(input, "ses_test")
	if len(events) != 2 || events[0].action != "command.executed" || events[1].action != "file.modified" {
		t.Fatalf("events = %#v", events)
	}
	file := events[1].fields["file"].(map[string]interface{})
	if file["operation"] != "delete" {
		t.Fatalf("file = %#v", file)
	}
	call := events[1].fields["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	if call["id"] != "call_rm" {
		t.Fatalf("call = %#v", call)
	}

	input["tool_response"] = map[string]interface{}{
		"output":   "failed",
		"metadata": map[string]interface{}{"exit": 1},
	}
	if failed := opencodeEndpointEvents(input, "ses_test"); len(failed) != 1 {
		t.Fatalf("failed rm emitted %d events, want command only", len(failed))
	}
	input["tool_response"] = map[string]interface{}{"output": "unknown", "metadata": map[string]interface{}{}}
	if unknown := opencodeEndpointEvents(input, "ses_test"); len(unknown) != 1 {
		t.Fatalf("rm without exit metadata emitted %d events, want command only", len(unknown))
	}
}

func TestOpenCodeSiblingEventsDoNotShareNestedFields(t *testing.T) {
	events := opencodeEndpointEvents(map[string]interface{}{
		"type":       "tool.execute.after",
		"session_id": "ses_test",
		"tool_name":  "bash",
		"call_id":    "call_rm",
		"tool_input": map[string]interface{}{"command": `rm "/repo/.tmp/e2e.txt"`},
		"tool_response": map[string]interface{}{
			"output":   "",
			"metadata": map[string]interface{}{"exit": 0},
		},
		"file_mutations": []interface{}{
			map[string]interface{}{"path": "/repo/.tmp/e2e.txt", "operation": "delete"},
		},
	}, "ses_test")
	if len(events) != 2 {
		t.Fatalf("events = %d", len(events))
	}
	firstCall := events[0].fields["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	delete(firstCall, "arguments")
	firstContent := events[0].fields["content"].(map[string]interface{})
	firstContent["included"] = false

	secondCall := events[1].fields["gen_ai"].(map[string]interface{})["tool"].(map[string]interface{})["call"].(map[string]interface{})
	if secondCall["arguments"] == nil {
		t.Fatal("mutating first event removed second event arguments")
	}
	secondContent := events[1].fields["content"].(map[string]interface{})
	if secondContent["included"] != true {
		t.Fatalf("mutating first event changed second content: %#v", secondContent)
	}
}

func TestOpenCodePermissionRepliesMapAllowAndDeny(t *testing.T) {
	for _, tt := range []struct {
		reply  string
		action string
	}{
		{reply: "once", action: "approval.allowed"},
		{reply: "always", action: "approval.allowed"},
		{reply: "reject", action: "approval.denied"},
		{reply: "timeout", action: "approval.denied"},
		{reply: "unknown", action: "approval.requested"},
	} {
		action, _, _, _, _ := opencodeEndpointEvent(map[string]interface{}{
			"type":       "permission.replied",
			"session_id": "ses_test",
			"properties": map[string]interface{}{
				"reply": tt.reply, "permission": "bash",
				"tool": map[string]interface{}{"messageID": "msg_1", "callID": "call_permission"},
			},
		}, "ses_test")
		if action != tt.action {
			t.Fatalf("reply %q action = %q, want %q", tt.reply, action, tt.action)
		}
	}
}

func TestOpenCodeTidyPlanetTraceReplay(t *testing.T) {
	payloads := []map[string]interface{}{
		{"type": "chat.message", "session_id": "ses_tidy", "output": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"type": "text", "text": "hi kimi"}}}},
		{"type": "chat.message", "session_id": "ses_tidy", "output": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"type": "text", "text": "summarize this repo"}}}},
		{"type": "tool.execute.before", "session_id": "ses_tidy", "tool_name": "read", "call_id": "call_read", "tool_input": map[string]interface{}{"filePath": "/repo/README.md"}},
		{"type": "tool.execute.after", "session_id": "ses_tidy", "tool_name": "read", "call_id": "call_read", "tool_input": map[string]interface{}{"filePath": "/repo/README.md"}, "tool_response": map[string]interface{}{"output": "Beacon"}},
		{"type": "tool.execute.before", "session_id": "ses_tidy", "tool_name": "glob", "call_id": "call_glob", "tool_input": map[string]interface{}{"pattern": "**/README.md"}},
		{"type": "tool.execute.after", "session_id": "ses_tidy", "tool_name": "glob", "call_id": "call_glob", "tool_input": map[string]interface{}{"pattern": "**/README.md"}, "tool_response": map[string]interface{}{"output": "/repo/README.md"}},
		{"type": "chat.message", "session_id": "ses_tidy", "output": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"type": "text", "text": "look up NYC weather"}}}},
		{"type": "tool.execute.before", "session_id": "ses_tidy", "tool_name": "webfetch", "call_id": "call_web", "tool_input": map[string]interface{}{"url": "https://example.com/weather"}},
		{"type": "tool.execute.after", "session_id": "ses_tidy", "tool_name": "webfetch", "call_id": "call_web", "tool_input": map[string]interface{}{"url": "https://example.com/weather"}, "tool_response": map[string]interface{}{"output": "sunny"}},
		{"type": "chat.message", "session_id": "ses_tidy", "output": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"type": "text", "text": "persist this to a local file"}}}},
		{"type": "tool.execute.before", "session_id": "ses_tidy", "tool_name": "write", "call_id": "call_write", "tool_input": map[string]interface{}{"filePath": "/repo/weather.md", "content": "sunny"}},
		{"type": "tool.execute.after", "session_id": "ses_tidy", "tool_name": "write", "call_id": "call_write", "tool_input": map[string]interface{}{"filePath": "/repo/weather.md", "content": "sunny"}, "tool_response": map[string]interface{}{"output": "ok"}},
		{"type": "chat.message", "session_id": "ses_tidy", "output": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"type": "text", "text": "now delete it"}}}},
		{"type": "tool.execute.before", "session_id": "ses_tidy", "tool_name": "bash", "call_id": "call_delete", "tool_input": map[string]interface{}{"command": "rm /repo/weather.md"}},
		{"type": "tool.execute.after", "session_id": "ses_tidy", "tool_name": "bash", "call_id": "call_delete", "tool_input": map[string]interface{}{"command": "rm /repo/weather.md"}, "tool_response": map[string]interface{}{"output": "", "metadata": map[string]interface{}{"exitCode": 0}}},
	}
	var actions []string
	var calls = map[string]int{}
	for _, payload := range payloads {
		for _, event := range opencodeEndpointEvents(payload, "ses_tidy") {
			actions = append(actions, event.action)
			genAI, _ := event.fields["gen_ai"].(map[string]interface{})
			tool, _ := genAI["tool"].(map[string]interface{})
			call, _ := tool["call"].(map[string]interface{})
			if id, _ := call["id"].(string); id != "" {
				calls[id]++
			}
		}
	}
	want := []string{
		"prompt.submitted", "prompt.submitted",
		"tool.invoked", "file.read",
		"tool.invoked", "tool.completed",
		"prompt.submitted", "tool.invoked", "tool.completed",
		"prompt.submitted", "tool.invoked", "file.modified",
		"prompt.submitted", "tool.invoked", "command.executed",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions:\n got %v\nwant %v", actions, want)
	}
	for _, id := range []string{"call_read", "call_glob", "call_web", "call_write", "call_delete"} {
		if calls[id] != 2 {
			t.Fatalf("%s appears %d times, want invocation and terminal event", id, calls[id])
		}
	}
}

func TestOpenCodeForwardedEventsMatchGoIngestion(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "beacon", "internal", "endpoint", "hooks", "assets", "opencode", "beacon.ts"))
	if err != nil {
		t.Fatalf("read embedded plugin source: %v", err)
	}
	got := forwardedEventsFromPluginSource(t, string(source))
	want := supportedOpenCodeEventTypes()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forwarded events do not match Go ingestion\nplugin=%#v\ngo=%#v", got, want)
	}
}

func assertNoEndpointLog(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("endpoint log should not exist for unsupported event: %s", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected endpoint log stat error: %v", err)
	}
}

func forwardedEventsFromPluginSource(t *testing.T, source string) []string {
	t.Helper()
	var events []string
	for _, setName := range []string{"directHookTypes", "forwardedEvents"} {
		re := regexp.MustCompile(`(?s)` + setName + ` = new Set\(\[(.*?)\]\)`)
		match := re.FindStringSubmatch(source)
		if len(match) != 2 {
			t.Fatalf("%s list not found in plugin source", setName)
		}
		itemRE := regexp.MustCompile(`"([^"]+)"`)
		matches := itemRE.FindAllStringSubmatch(match[1], -1)
		for _, match := range matches {
			events = append(events, match[1])
		}
	}
	return events
}

func readOpenCodeFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "opencode", name))
	if err != nil {
		t.Fatalf("read opencode fixture %s: %v", name, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode fixture %s: %v", name, err)
	}
	return payload
}
