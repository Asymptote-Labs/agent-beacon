package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// openClawEnvelope builds the envelope shape the Beacon OpenClaw plugin writes to this binary's
// stdin, so every test below exercises the same contract the plugin has to honour.
func openClawEnvelope(hook string, event map[string]interface{}, identity map[string]interface{}) map[string]interface{} {
	envelope := map[string]interface{}{
		"hook":      hook,
		"sessionId": "sess-1",
		"cwd":       "/workspace/repo",
		"event":     event,
	}
	for key, value := range identity {
		envelope[key] = value
	}
	return envelope
}

func openClawEvents(t *testing.T, hook string, event map[string]interface{}, identity map[string]interface{}) []normalizedEvent {
	t.Helper()
	input := openClawEnvelope(hook, event, identity)
	return openClawEndpointEvents(input, resolveSessionID(input, openClawPlatform))
}

func openClawSingle(t *testing.T, hook string, event map[string]interface{}, identity map[string]interface{}) normalizedEvent {
	t.Helper()
	events := openClawEvents(t, hook, event, identity)
	if len(events) != 1 {
		t.Fatalf("%s: got %d events, want 1: %+v", hook, len(events), events)
	}
	return events[0]
}

// The plugin's registration list and this mapper's switch are two halves of one contract, and a
// mismatch is silent: an unregistered hook simply never fires, and an unmapped one produces no
// event. Every supported hook must therefore still resolve to a case here.
func TestOpenClawSupportedHooksAllMap(t *testing.T) {
	payloads := map[string]map[string]interface{}{
		"session_start":     {"sessionId": "sess-1"},
		"session_end":       {"sessionId": "sess-1", "messageCount": 4},
		"message_received":  {"content": "ship the release", "from": "U123"},
		"before_tool_call":  {"toolName": "exec", "params": map[string]interface{}{"command": "ls"}},
		"after_tool_call":   {"toolName": "exec", "params": map[string]interface{}{"command": "ls"}},
		"llm_output":        {"usage": map[string]interface{}{"input": 10, "output": 5}},
		"before_compaction": {"messageCount": 40},
		"after_compaction":  {"messageCount": 12, "compactedCount": 28},
		"subagent_spawned":  {"childSessionKey": "child-1", "agentId": "researcher", "mode": "run"},
		"subagent_ended":    {"targetSessionKey": "child-1", "targetKind": "subagent", "reason": "done"},
	}
	for _, hook := range supportedOpenClawHooks() {
		payload, ok := payloads[hook]
		if !ok {
			t.Fatalf("hook %q has no fixture; add one when adding the hook", hook)
		}
		if got := openClawEvents(t, hook, payload, nil); len(got) == 0 {
			t.Fatalf("hook %q produced no events", hook)
		}
	}
}

// An OpenClaw hook the plugin does not register must not become a generic row if it ever reaches
// this binary by another route.
func TestOpenClawUnknownHookProducesNothing(t *testing.T) {
	for _, hook := range []string{"llm_input", "agent_end", "model_call_ended", "message_sent", ""} {
		if got := openClawEvents(t, hook, map[string]interface{}{"content": "x"}, nil); len(got) != 0 {
			t.Fatalf("hook %q produced %d events, want 0", hook, len(got))
		}
	}
}

func TestOpenClawSessionLifecycle(t *testing.T) {
	start := openClawSingle(t, "session_start", map[string]interface{}{
		"sessionId": "sess-1", "resumedFrom": "sess-0",
	}, nil)
	if start.action != "session.started" {
		t.Fatalf("action = %q, want session.started", start.action)
	}
	if got := nested(t, start.fields, "session")["id"]; got != "sess-1" {
		t.Fatalf("session.id = %v, want sess-1", got)
	}
	if got := nested(t, start.fields, "raw")["openclaw_resumed_from"]; got != "sess-0" {
		t.Fatalf("resumed_from = %v, want sess-0", got)
	}

	end := openClawSingle(t, "session_end", map[string]interface{}{
		"sessionId": "sess-1", "reason": "compaction", "nextSessionId": "sess-2", "messageCount": 9,
	}, nil)
	if end.action != "session.ended" {
		t.Fatalf("action = %q, want session.ended", end.action)
	}
	raw := nested(t, end.fields, "raw")
	if raw["openclaw_session_end_reason"] != "compaction" {
		t.Fatalf("end reason = %v, want compaction", raw["openclaw_session_end_reason"])
	}
	if raw["openclaw_next_session_id"] != "sess-2" {
		t.Fatalf("next session = %v, want sess-2", raw["openclaw_next_session_id"])
	}
}

// sessionKey is the conversation, not the session. Reading it as the session id would merge every
// compaction generation of one conversation into a single session, which is exactly what the
// nextSessionId/previousSessionId pair exists to keep distinct.
func TestOpenClawSessionKeyIsNotTheSessionID(t *testing.T) {
	input := map[string]interface{}{
		"hook":       "session_start",
		"sessionKey": "discord:chan-9",
		"event":      map[string]interface{}{},
	}
	if got := resolveSessionID(input, openClawPlatform); got != "" {
		t.Fatalf("resolveSessionID from sessionKey alone = %q, want empty", got)
	}
	events := openClawEndpointEvents(input, "")
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if got := nested(t, events[0].fields, "raw")["openclaw_session_key"]; got != "discord:chan-9" {
		t.Fatalf("raw session key = %v, want discord:chan-9", got)
	}
}

func TestOpenClawPromptFromMessageReceived(t *testing.T) {
	event := openClawSingle(t, "message_received", map[string]interface{}{
		"content": "deploy staging", "from": "U42",
	}, map[string]interface{}{"channel": "slack", "runId": "run-7"})

	if event.action != "prompt.submitted" || event.category != "prompt" {
		t.Fatalf("action/category = %q/%q", event.action, event.category)
	}
	if got := nested(t, event.fields, "prompt")["text"]; got != "deploy staging" {
		t.Fatalf("prompt.text = %v", got)
	}
	messages, ok := nested(t, event.fields, "gen_ai", "input")["messages"]
	if !ok {
		t.Fatal("gen_ai.input.messages missing")
	}
	if len(messages.([]interface{})) == 0 {
		t.Fatal("gen_ai.input.messages is empty")
	}
	raw := nested(t, event.fields, "raw")
	if raw["openclaw_sender"] != "U42" {
		t.Fatalf("sender = %v, want U42", raw["openclaw_sender"])
	}
	if raw["openclaw_channel"] != "slack" {
		t.Fatalf("channel = %v, want slack", raw["openclaw_channel"])
	}
	if raw["openclaw_run_id"] != "run-7" {
		t.Fatalf("run id = %v, want run-7", raw["openclaw_run_id"])
	}
	if _, ok := event.fields["content"]; !ok {
		t.Fatal("retained content fields missing")
	}
}

func TestOpenClawEmptyPromptProducesNothing(t *testing.T) {
	if got := openClawEvents(t, "message_received", map[string]interface{}{"from": "U42"}, nil); len(got) != 0 {
		t.Fatalf("got %d events for a contentless message, want 0", len(got))
	}
}

func TestOpenClawExecCommand(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName":   "exec",
		"toolCallId": "call-1",
		"params":     map[string]interface{}{"command": "git push origin main", "workdir": "/workspace/repo"},
		"durationMs": 1200,
		"result": map[string]interface{}{
			"details": map[string]interface{}{"status": "completed", "exitCode": 0, "aggregated": "Everything up-to-date"},
		},
	}, nil)

	if event.action != "command.executed" || event.category != "command" {
		t.Fatalf("action/category = %q/%q", event.action, event.category)
	}
	command := nested(t, event.fields, "command")
	if command["command"] != "git push origin main" {
		t.Fatalf("command = %v", command["command"])
	}
	if command["exit_code"] != 0 {
		t.Fatalf("exit_code = %v, want 0", command["exit_code"])
	}
	if command["output"] != "Everything up-to-date" {
		t.Fatalf("output = %v", command["output"])
	}
	if event.severity != "info" {
		t.Fatalf("severity = %q, want info for a zero exit", event.severity)
	}
	if got := nested(t, event.fields, "gen_ai", "tool", "call")["id"]; got != "call-1" {
		t.Fatalf("gen_ai.tool.call.id = %v, want call-1", got)
	}
}

func TestOpenClawFailedCommandRaisesSeverity(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "exec",
		"params":   map[string]interface{}{"command": "make test"},
		"result": map[string]interface{}{
			"details": map[string]interface{}{"status": "failed", "exitCode": 2, "aggregated": "FAIL"},
		},
	}, nil)
	if event.severity != "medium" {
		t.Fatalf("severity = %q, want medium for a non-zero exit", event.severity)
	}
}

// A backgrounded exec reports `status: "running"` with a partial tail and no exit code. Recording
// the tail as the command's output would state an outcome the runtime has not reached.
func TestOpenClawRunningExecRecordsNoOutcome(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "exec",
		"params":   map[string]interface{}{"command": "npm run dev"},
		"result": map[string]interface{}{
			"details": map[string]interface{}{"status": "running", "pid": 4242, "tail": "listening on 3000"},
		},
	}, nil)
	command := nested(t, event.fields, "command")
	if _, ok := command["exit_code"]; ok {
		t.Fatalf("exit_code present for a running command: %v", command)
	}
	if _, ok := command["output"]; ok {
		t.Fatalf("output present for a running command: %v", command)
	}
}

func TestOpenClawFileTools(t *testing.T) {
	read := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "read", "params": map[string]interface{}{"path": "/workspace/repo/main.go"},
	}, nil)
	if read.action != "file.read" {
		t.Fatalf("read action = %q", read.action)
	}
	file := nested(t, read.fields, "file")
	if file["path"] != "/workspace/repo/main.go" || file["operation"] != "read" || file["language"] != "go" {
		t.Fatalf("file = %+v", file)
	}

	write := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "write",
		"params":   map[string]interface{}{"path": "/workspace/repo/new.txt", "content": "hello\n"},
	}, nil)
	if write.action != "file.created" {
		t.Fatalf("write action = %q", write.action)
	}
	if nested(t, write.fields, "file")["operation"] != "create" {
		t.Fatalf("write operation = %v", nested(t, write.fields, "file")["operation"])
	}
	if _, ok := write.fields["content"]; !ok {
		t.Fatal("write produced no retained content")
	}
	if diff, _ := nested(t, write.fields, "file")["diff"].(string); !strings.Contains(diff, "+hello") {
		t.Fatalf("write diff does not carry the new content: %q", diff)
	}

	edit := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "edit",
		"params":   map[string]interface{}{"path": "/workspace/repo/main.go", "oldText": "a := 1", "newText": "a := 2"},
	}, nil)
	if edit.action != "file.modified" {
		t.Fatalf("edit action = %q", edit.action)
	}
	editFile := nested(t, edit.fields, "file")
	diff, _ := editFile["diff"].(string)
	if !strings.Contains(diff, "-a := 1") || !strings.Contains(diff, "+a := 2") {
		t.Fatalf("edit diff does not describe the change: %q", diff)
	}
	if editFile["diff_bytes"] != len(diff) {
		t.Fatalf("diff_bytes = %v, want %d", editFile["diff_bytes"], len(diff))
	}
	if hash, _ := editFile["diff_hash"].(string); len(hash) != 64 {
		t.Fatalf("diff_hash = %q, want a sha256 hex digest", hash)
	}
	if _, ok := edit.fields["content"]; !ok {
		t.Fatal("edit produced no retained content marker beside its diff")
	}
}

// An edit whose two sides match changed nothing, so there is no diff to record; the file event
// still carries its path.
func TestOpenClawNoopEditHasNoDiff(t *testing.T) {
	edit := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "edit",
		"params":   map[string]interface{}{"path": "/workspace/repo/main.go", "oldText": "same", "newText": "same"},
	}, nil)
	if _, ok := edit.fields["content"]; ok {
		t.Fatalf("no-op edit produced content: %+v", edit.fields["content"])
	}
	if nested(t, edit.fields, "file")["path"] != "/workspace/repo/main.go" {
		t.Fatal("no-op edit lost its path")
	}
}

// A file tool whose path argument did not resolve is not a file action. Recording one would
// produce a row every file-scoped query matches and none can explain.
func TestOpenClawFileToolWithoutPathFallsBackToTool(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "read", "params": map[string]interface{}{},
	}, nil)
	if event.action != "tool.completed" || event.category != "tool" {
		t.Fatalf("action/category = %q/%q, want tool.completed/tool", event.action, event.category)
	}
}

func TestOpenClawApplyPatchBecomesOneEventPerPath(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.go\n*** End Patch\n"
	events := openClawEvents(t, "after_tool_call", map[string]interface{}{
		"toolName":     "apply_patch",
		"toolCallId":   "call-9",
		"params":       map[string]interface{}{"input": patch},
		"derivedPaths": []interface{}{"/workspace/repo/a.go", "/workspace/repo/b.go"},
	}, nil)

	if len(events) != 2 {
		t.Fatalf("got %d events, want one per derived path", len(events))
	}
	for i, want := range []string{"/workspace/repo/a.go", "/workspace/repo/b.go"} {
		if events[i].action != "file.modified" {
			t.Fatalf("event %d action = %q", i, events[i].action)
		}
		if got := nested(t, events[i].fields, "file")["path"]; got != want {
			t.Fatalf("event %d path = %v, want %v", i, got, want)
		}
		if got := nested(t, events[i].fields, "gen_ai", "tool", "call")["id"]; got != "call-9" {
			t.Fatalf("event %d lost the tool call id", i)
		}
	}
	// The patch text is retained once rather than copied onto every path's event.
	if _, ok := events[0].fields["content"]; !ok {
		t.Fatal("first patch event carries no retained patch")
	}
	if _, ok := events[1].fields["content"]; ok {
		t.Fatal("patch text repeated on the second event")
	}
}

// Without derived paths there is nothing to name, so the call is recorded as a completed tool
// carrying its patch rather than as a file change with an invented path.
func TestOpenClawApplyPatchWithoutDerivedPathsRecordsNoFile(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "apply_patch",
		"params":   map[string]interface{}{"input": "*** Begin Patch\n*** End Patch\n"},
	}, nil)
	if _, ok := event.fields["file"]; ok {
		t.Fatalf("file field invented without derived paths: %+v", event.fields["file"])
	}
	if event.action != "tool.completed" {
		t.Fatalf("action = %q, want tool.completed", event.action)
	}
	if _, ok := event.fields["content"]; !ok {
		t.Fatal("patch text not retained")
	}
}

func TestOpenClawMCPToolAttribution(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "github__create_issue",
		"params":   map[string]interface{}{"title": "bug"},
	}, nil)
	if event.action != "mcp.tool_invoked" || event.category != "mcp" {
		t.Fatalf("action/category = %q/%q", event.action, event.category)
	}
	mcp := nested(t, event.fields, "mcp")
	if mcp["server"] != "github" || mcp["tool"] != "create_issue" {
		t.Fatalf("mcp = %+v", mcp)
	}
}

// OpenClaw's own Codex and Claude harness bridges mint Claude-style `mcp__server__tool` names.
// Splitting those on the first separator would report the server as "mcp".
func TestOpenClawClaudeStyleMCPName(t *testing.T) {
	server, tool := openClawMCPServerTool("mcp__openclaw__automations")
	if server != "openclaw" || tool != "automations" {
		t.Fatalf("server/tool = %q/%q", server, tool)
	}
}

func TestOpenClawBuiltinToolIsNotMCP(t *testing.T) {
	for _, name := range []string{"exec", "read", "apply_patch", "ls", "process", "", "trailing__"} {
		if server, tool := openClawMCPServerTool(name); server != "" || tool != "" {
			t.Fatalf("%q read as MCP server %q tool %q", name, server, tool)
		}
	}
}

func TestOpenClawToolFailure(t *testing.T) {
	event := openClawSingle(t, "after_tool_call", map[string]interface{}{
		"toolName": "write",
		"params":   map[string]interface{}{"path": "/workspace/repo/x.txt", "content": "y"},
		"error":    "EACCES: permission denied",
	}, nil)
	if event.action != "tool.failed" || event.severity != "high" {
		t.Fatalf("action/severity = %q/%q", event.action, event.severity)
	}
	if got := nested(t, event.fields, "error")["message"]; got != "EACCES: permission denied" {
		t.Fatalf("error.message = %v", got)
	}
}

func TestOpenClawLlmOutputUsageAndMessage(t *testing.T) {
	events := openClawEvents(t, "llm_output", map[string]interface{}{
		"assistantTexts":     []interface{}{"pushed", "and tagged"},
		"resolvedRef":        "openai/gpt-5",
		"contextTokenBudget": 200000,
		"usage": map[string]interface{}{
			"input": 1200, "output": 300, "cacheRead": 800, "cacheWrite": 64, "total": 1500,
		},
	}, nil)
	if len(events) != 2 {
		t.Fatalf("got %d events, want an agent.message and a token.usage", len(events))
	}

	message := events[0]
	if message.action != "agent.message" {
		t.Fatalf("first action = %q", message.action)
	}
	if message.fields["model"] != "openai/gpt-5" {
		t.Fatalf("model = %v, want the provider-prefixed ref", message.fields["model"])
	}

	usageEvent := events[1]
	if usageEvent.action != "token.usage" {
		t.Fatalf("second action = %q", usageEvent.action)
	}
	usage := nested(t, usageEvent.fields, "gen_ai", "usage")
	if usage["input_tokens"] != 1200 || usage["output_tokens"] != 300 {
		t.Fatalf("usage = %+v", usage)
	}
	if got := nested(t, usageEvent.fields, "gen_ai", "usage", "cache_read")["input_tokens"]; got != 800 {
		t.Fatalf("cache_read = %v, want 800", got)
	}
	if got := nested(t, usageEvent.fields, "gen_ai", "usage", "cache_creation")["input_tokens"]; got != 64 {
		t.Fatalf("cache_creation = %v, want 64", got)
	}
	// `total` is redundant with its own parts and has no field in the canonical shape.
	for key := range usage {
		if key == "total" || key == "total_tokens" {
			t.Fatalf("usage carries a total: %+v", usage)
		}
	}
	// Cost is never derived locally, and OpenClaw reports none on this hook.
	if _, ok := usage["cost_usd"]; ok {
		t.Fatalf("usage carries a cost OpenClaw did not report: %+v", usage)
	}
	context := nested(t, usageEvent.fields, "gen_ai", "context")
	if context["limit_tokens"] != 200000 || context["used_tokens"] != 1200 {
		t.Fatalf("context = %+v", context)
	}
}

func TestOpenClawLlmOutputWithoutUsageOrTextProducesNothing(t *testing.T) {
	if got := openClawEvents(t, "llm_output", map[string]interface{}{"provider": "openai"}, nil); len(got) != 0 {
		t.Fatalf("got %d events, want 0", len(got))
	}
}

func TestOpenClawCompaction(t *testing.T) {
	before := openClawSingle(t, "before_compaction", map[string]interface{}{"messageCount": 80}, nil)
	if before.action != "session.compacting" {
		t.Fatalf("action = %q", before.action)
	}
	after := openClawSingle(t, "after_compaction", map[string]interface{}{
		"messageCount": 12, "compactedCount": 68, "previousSessionId": "sess-0",
	}, nil)
	if after.action != "session.compacted" {
		t.Fatalf("action = %q", after.action)
	}
	raw := nested(t, after.fields, "raw")
	if raw["openclaw_compacted_count"] != 68 || raw["openclaw_previous_session_id"] != "sess-0" {
		t.Fatalf("raw = %+v", raw)
	}
}

func TestOpenClawSubagentLifecycle(t *testing.T) {
	spawned := openClawSingle(t, "subagent_spawned", map[string]interface{}{
		"childSessionKey": "child-1", "agentId": "researcher", "mode": "run",
		"resolvedModel": "anthropic/claude-opus-5",
	}, nil)
	if spawned.action != "subagent.started" {
		t.Fatalf("action = %q", spawned.action)
	}
	if spawned.fields["model"] != "anthropic/claude-opus-5" {
		t.Fatalf("model = %v, want the child's resolved model", spawned.fields["model"])
	}

	failed := openClawSingle(t, "subagent_ended", map[string]interface{}{
		"targetSessionKey": "child-1", "targetKind": "subagent",
		"reason": "error", "outcome": "error", "error": "provider timeout",
	}, nil)
	if failed.action != "subagent.stopped" || failed.severity != "high" {
		t.Fatalf("action/severity = %q/%q", failed.action, failed.severity)
	}
	if got := nested(t, failed.fields, "error")["message"]; got != "provider timeout" {
		t.Fatalf("error.message = %v", got)
	}
}

// OpenClaw exposes no operator approval decision, and the one thing on this runtime that reads
// like one -- an exec result's model-backed `approvalReviewOutcome` -- is an LLM's opinion, not a
// person's. Neither may produce an approval event.
func TestOpenClawNeverSynthesizesApprovals(t *testing.T) {
	events := openClawEvents(t, "after_tool_call", map[string]interface{}{
		"toolName": "exec",
		"params":   map[string]interface{}{"command": "rm -rf /tmp/build"},
		"result": map[string]interface{}{
			"details": map[string]interface{}{
				"status": "completed", "exitCode": 0, "aggregated": "",
				"approvalReviewOutcome": "approved",
				"approvalReviews": []interface{}{
					map[string]interface{}{"id": "r1", "label": "risk", "status": "approved", "rationale": "safe path"},
				},
			},
		},
	}, nil)
	for _, event := range events {
		if strings.HasPrefix(event.action, "approval.") {
			t.Fatalf("synthesized an approval from a model-backed review: %+v", event)
		}
		if _, ok := event.fields["approval"]; ok {
			t.Fatalf("event carries an approval block: %+v", event.fields["approval"])
		}
	}
	// The review is still searchable, in raw with the rest of the payload.
	body, err := json.Marshal(events[0].fields["raw"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "approvalReviewOutcome") {
		t.Fatalf("model-backed review dropped from raw: %s", body)
	}
}

// A tool argument must never be able to overwrite the identity the envelope carries. This is the
// reason the plugin nests the hook event instead of spreading it.
func TestOpenClawToolParamsCannotOverwriteIdentity(t *testing.T) {
	event := openClawSingle(t, "before_tool_call", map[string]interface{}{
		"toolName": "exec",
		"params": map[string]interface{}{
			"command": "echo hi", "sessionId": "attacker-session", "cwd": "/etc",
		},
	}, nil)
	if got := nested(t, event.fields, "session")["id"]; got != "sess-1" {
		t.Fatalf("session.id = %v, want the envelope's sess-1", got)
	}
	if got := nested(t, event.fields, "session")["working_directory"]; got != "/workspace/repo" {
		t.Fatalf("working_directory = %v, want the envelope's", got)
	}
}

func TestOpenClawWorkspaceFields(t *testing.T) {
	event := openClawSingle(t, "session_start", map[string]interface{}{"sessionId": "sess-1"}, nil)
	if event.fields["repository"] != "/workspace/repo" {
		t.Fatalf("repository = %v", event.fields["repository"])
	}
	if got := nested(t, event.fields, "session")["working_directory"]; got != "/workspace/repo" {
		t.Fatalf("working_directory = %v", got)
	}
}

// A chat turn that never touched a workspace genuinely has none. Falling back to the gateway
// daemon's own directory would attribute the work to wherever it happened to be started.
func TestOpenClawAbsentWorkspaceStaysAbsent(t *testing.T) {
	input := map[string]interface{}{
		"hook":      "session_start",
		"sessionId": "sess-1",
		"event":     map[string]interface{}{},
	}
	events := openClawEndpointEvents(input, "sess-1")
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if value, ok := events[0].fields["repository"]; ok && value != "" {
		t.Fatalf("repository invented without a workspace: %v", value)
	}
}

func TestOpenClawCollectionMethodIsPlugin(t *testing.T) {
	if got := asymptoteobserve.CollectionMethodForPlatform(openClawPlatform); got != asymptoteobserve.CollectionMethodPlugin {
		t.Fatalf("collection method = %q, want %q", got, asymptoteobserve.CollectionMethodPlugin)
	}
}

func TestOpenClawHarnessName(t *testing.T) {
	for _, spelling := range []string{"openclaw", "OpenClaw", "openclaw_gateway", "open-claw"} {
		if got := asymptoteobserve.NormalizeHarnessName(spelling); got != "openclaw_gateway" {
			t.Fatalf("NormalizeHarnessName(%q) = %q, want openclaw_gateway", spelling, got)
		}
	}
}

// The contract between the managed plugin's registration list and this mapper's switch.
//
// A name misspelled on either side produces no error anywhere -- OpenClaw dispatches nothing, the
// mapper is never called, and the only symptom is a category of agent activity silently missing
// from the log. Read out of the shipped plugin source rather than duplicated here, so this test
// cannot pass against a list that only exists in the test.
func TestOpenClawPluginRegistrationsMatchTheMapper(t *testing.T) {
	registered := openClawSubscribedHooks(t)
	want := append([]string(nil), supportedOpenClawHooks()...)
	sort.Strings(want)
	sort.Strings(registered)

	if len(registered) != len(want) {
		t.Fatalf("the plugin registers %v but the mapper handles %v", registered, want)
	}
	for i := range registered {
		if registered[i] != want[i] {
			t.Fatalf("the plugin registers %v but the mapper handles %v; a name on either side "+
				"that the other does not have produces no telemetry and no error", registered, want)
		}
	}
}

// Registering any of these would ask OpenClaw for a power Beacon must not hold: rewriting a
// prompt, claiming a message before the agent sees it, gating an install, or voting on whether a
// skill may be created. Asserted against the shipped source, not just the plugin's own test,
// because this is the property that keeps Beacon an observer.
func TestOpenClawPluginRegistersNoMutatingHook(t *testing.T) {
	registered := map[string]bool{}
	for _, hook := range openClawSubscribedHooks(t) {
		registered[hook] = true
	}
	for _, forbidden := range []string{
		"before_prompt_build", "agent_turn_prepare", "heartbeat_prompt_contribution",
		"before_agent_reply", "before_agent_run", "before_agent_finalize",
		"inbound_claim", "before_dispatch", "reply_dispatch",
		"message_sending", "reply_payload_sending", "before_message_write",
		"tool_result_persist", "resolve_exec_env", "before_install",
		"skill_proposal_evaluate", "subagent_delivery_target", "before_model_resolve",
	} {
		if registered[forbidden] {
			t.Fatalf("the OpenClaw plugin registers %q, which can change what the agent does", forbidden)
		}
	}
}

// openClawSubscribedHooks reads the `subscribedHooks` array out of the shipped plugin source.
func openClawSubscribedHooks(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "plugins", "openclaw-beacon", "src", "beacon.js")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("plugin source is unreadable: %v", err)
	}
	block := regexp.MustCompile(`(?s)const subscribedHooks = \[(.*?)\n\]`).FindSubmatch(data)
	if len(block) != 2 {
		t.Fatalf("%s has no subscribedHooks array; the mapper's contract is with that list", path)
	}
	var names []string
	for _, match := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(block[1], -1) {
		names = append(names, string(match[1]))
	}
	if len(names) == 0 {
		t.Fatalf("%s registers no hooks", path)
	}
	return names
}
