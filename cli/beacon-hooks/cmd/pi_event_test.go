package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// setupPiEvent puts a Pi hook run in a sandbox: config directories under a temp dir, a temp runtime
// log, and full content retention so the retained-text assertions have something to inspect.
func setupPiEvent(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "pi"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	// Git enrichment shells out to the real git in whatever directory the payload names, which makes
	// the result depend on the machine running the suite.
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	return logPath
}

// requireNoEndpointEvents asserts that a payload produced no telemetry at all. Checked by the log's
// absence rather than by an empty-file read, because the sink creates the file only on first write.
func requireNoEndpointEvents(t *testing.T, logPath string) {
	t.Helper()
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("an event was written to %s, want none", logPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat endpoint log: %v", err)
	}
}

func eventAction(t *testing.T, event map[string]interface{}) string {
	t.Helper()
	meta, ok := event["event"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no event object: %#v", event)
	}
	action, _ := meta["action"].(string)
	return action
}

func nestedMap(t *testing.T, event map[string]interface{}, keys ...string) map[string]interface{} {
	t.Helper()
	current := event
	for _, key := range keys {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			t.Fatalf("event is missing %v (stopped at %q): %#v", keys, key, event)
		}
		current = next
	}
	return current
}

func TestPiEventRecordsSessionStart(t *testing.T) {
	logPath := setupPiEvent(t)

	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "session_start",
		"session_id": "pi-session-1",
		"cwd":        "/tmp/project",
		"reason":     "startup",
		"model":      "anthropic/claude-sonnet-4",
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "session.started" {
		t.Fatalf("event.action = %q, want session.started", got)
	}
	if got := nestedMap(t, event, "session")["id"]; got != "pi-session-1" {
		t.Fatalf("session.id = %q, want pi-session-1", got)
	}
	if got := nestedMap(t, event, "session")["working_directory"]; got != "/tmp/project" {
		t.Fatalf("session.working_directory = %q, want /tmp/project", got)
	}
	if got := event["model"]; got != "anthropic/claude-sonnet-4" {
		t.Fatalf("model = %q, want the Pi model", got)
	}
}

// The canonical harness name has to appear on Pi events for the same reason discovery reports it:
// events grouped by harness.name must not split one runtime across two spellings.
func TestPiEventRecordsCanonicalHarnessName(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "session_start",
		"session_id": "pi-session-1",
	})

	event := lastEndpointEvent(t, logPath)
	if got := nestedMap(t, event, "harness")["name"]; got != "pi_cli" {
		t.Fatalf("harness.name = %q, want pi_cli", got)
	}
}

// Running pi-event is itself the statement that the platform is Pi. A hand-written invocation, or
// an installed command whose --platform was lost, must still classify as Pi rather than silently
// inheriting the default and misreporting the runtime.
func TestPiEventPinsPlatformRegardlessOfFlag(t *testing.T) {
	logPath := setupPiEvent(t)
	platformFlag = "claude"

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "session_start",
		"session_id": "pi-session-1",
	})

	event := lastEndpointEvent(t, logPath)
	if got := nestedMap(t, event, "harness")["name"]; got != "pi_cli" {
		t.Fatalf("harness.name = %q, want pi_cli even though --platform said claude", got)
	}
}

func TestPiEventRecordsPromptWithRedaction(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "input",
		"session_id": "pi-session-1",
		"cwd":        "/tmp/project",
		"prompt":     "deploy with token=pi-secret",
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", got)
	}
	if got := nestedMap(t, event, "prompt")["text"]; got != "deploy with token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want the secret redacted", got)
	}
	// The content marker is computed against the original text, so it describes what the runtime
	// produced rather than what the sink stored.
	content := nestedMap(t, event, "content")
	if content["redacted"] != true {
		t.Fatalf("content.redacted = %#v, want true", content["redacted"])
	}
	if content["included"] != true {
		t.Fatalf("content.included = %#v, want true", content["included"])
	}
}

// An input event with no text is dropped rather than written as an empty prompt: a prompt.submitted
// carrying no prompt is indistinguishable from a capture failure when someone reads the log.
func TestPiEventDropsEmptyPrompt(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "input",
		"session_id": "pi-session-1",
		"prompt":     "",
	})

	requireNoEndpointEvents(t, logPath)
}

// tool_call fires before the tool runs. Recording a shell call as command.executed here would
// report a command that may still be blocked and never run.
func TestPiEventToolCallIsInvokedNotExecuted(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":         "tool_call",
		"session_id":   "pi-session-1",
		"cwd":          "/tmp/project",
		"tool_name":    "bash",
		"tool_call_id": "call-1",
		"tool_input":   map[string]interface{}{"command": "rm -rf build"},
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked for a pre-execution event", got)
	}
	if got := nestedMap(t, event, "command")["command"]; got != "rm -rf build" {
		t.Fatalf("command.command = %q, want the command", got)
	}
	call := nestedMap(t, event, "gen_ai", "tool", "call")
	if call["id"] != "call-1" {
		t.Fatalf("gen_ai.tool.call.id = %#v, want call-1", call["id"])
	}
	if _, ok := call["result"]; ok {
		t.Fatal("gen_ai.tool.call.result was set on a pre-execution event")
	}
}

func TestPiEventToolResultActions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolName   string
		toolInput  map[string]interface{}
		wantAction string
	}{
		{"shell", "bash", map[string]interface{}{"command": "ls"}, "command.executed"},
		{"read", "read", map[string]interface{}{"file_path": "/tmp/project/main.go"}, "file.read"},
		{"edit", "edit", map[string]interface{}{"file_path": "/tmp/project/main.go"}, "file.modified"},
		{"write", "write", map[string]interface{}{"file_path": "/tmp/project/new.go"}, "file.modified"},
		{"mcp", "mcp__github__list_issues", map[string]interface{}{}, "mcp.tool_invoked"},
		{"unknown", "sparkle", map[string]interface{}{}, "tool.completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := setupPiEvent(t)

			runHookWithInput(t, runPiEvent, map[string]interface{}{
				"type":       "tool_result",
				"session_id": "pi-session-1",
				"cwd":        "/tmp/project",
				"tool_name":  tc.toolName,
				"tool_input": tc.toolInput,
			})

			event := lastEndpointEvent(t, logPath)
			if got := eventAction(t, event); got != tc.wantAction {
				t.Fatalf("tool %q produced action %q, want %q", tc.toolName, got, tc.wantAction)
			}
		})
	}
}

// Pi's built-in tools are lowercase, which the default file-edit classifier -- Claude's exact
// capitalized names -- recognizes as nothing. Without a Pi branch every edit was a generic tool
// completion carrying no file.path, and a threat rule matching on file.path saw no file activity.
func TestPiEventFileEditCarriesPathAndDiff(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_result",
		"session_id": "pi-session-1",
		"cwd":        "/tmp/project",
		"tool_name":  "edit",
		"tool_input": map[string]interface{}{
			"file_path":  "/tmp/project/main.go",
			"old_string": "package main",
			"new_string": "package main // edited",
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	file := nestedMap(t, event, "file")
	if file["path"] != "/tmp/project/main.go" {
		t.Fatalf("file.path = %#v, want the edited path", file["path"])
	}
	if file["operation"] != "modify" {
		t.Fatalf("file.operation = %#v, want modify", file["operation"])
	}
	// The diff itself is summarized rather than stored raw here; the hash and byte count are what
	// prove a diff was computed at all.
	if file["diff_hash"] == nil || file["diff_hash"] == "" {
		t.Fatalf("file.diff_hash is empty; no diff was computed: %#v", file)
	}
}

// A file-editing tool that never said which file it touched must not be reported as a file event.
// file.path is what rules and queries match on, and an absent one on a file.modified event looks
// like a capture bug rather than a tool that reported nothing.
func TestPiEventFileEditWithoutPathFallsBackToToolCompleted(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_result",
		"session_id": "pi-session-1",
		"tool_name":  "edit",
		"tool_input": map[string]interface{}{"pattern": "*.go"},
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "tool.completed" {
		t.Fatalf("event.action = %q, want tool.completed when no path was reported", got)
	}
	if _, ok := event["file"]; ok {
		t.Fatalf("a file object was written with no path: %#v", event["file"])
	}
}

func TestPiEventCommandResultCarriesExitCodeAndDuration(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":          "tool_result",
		"session_id":    "pi-session-1",
		"cwd":           "/tmp/project",
		"tool_name":     "bash",
		"tool_input":    map[string]interface{}{"command": "go test ./..."},
		"tool_response": map[string]interface{}{"output": "ok", "exit_code": float64(2)},
		"duration_ms":   float64(1500),
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	command := nestedMap(t, event, "command")
	if command["command"] != "go test ./..." {
		t.Fatalf("command.command = %#v", command["command"])
	}
	if command["exit_code"] != float64(2) {
		t.Fatalf("command.exit_code = %#v, want 2", command["exit_code"])
	}
	if command["duration_ms"] != float64(1500) {
		t.Fatalf("command.duration_ms = %#v, want 1500", command["duration_ms"])
	}
}

func TestPiEventToolFailureIsRecordedAsFailed(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_result",
		"session_id": "pi-session-1",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "false"},
		"is_error":   true,
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
	if got := nestedMap(t, event, "error")["type"]; got != "tool_error" {
		t.Fatalf("error.type = %q, want tool_error", got)
	}
	if got := nestedMap(t, event, "event")["severity"]; got != nil && got != "high" {
		t.Fatalf("severity = %#v, want high", got)
	}
}

// A JSON string "true" must not read as success. An extension version that serializes the flag as
// text would otherwise turn every failed tool into a successful one, which is the direction that
// hides real activity.
func TestPiEventToolFailureAcceptsStringFlag(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_result",
		"session_id": "pi-session-1",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "false"},
		"isError":    "true",
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed for a string-encoded flag", got)
	}
}

// gen_ai.usage is Beacon's only token representation, and Pi reports the full breakdown including
// cache reads, cache writes and a runtime-computed cost. This is the event the token attribution
// rollups and `beacon token-usage` are built on.
func TestPiEventRecordsTokenUsage(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "message_end",
		"session_id": "pi-session-1",
		"cwd":        "/tmp/project",
		"model":      "anthropic/claude-sonnet-4",
		"usage": map[string]interface{}{
			"input":       float64(1200),
			"output":      float64(340),
			"cacheRead":   float64(900),
			"cacheWrite":  float64(120),
			"totalTokens": float64(1540),
			"cost": map[string]interface{}{
				"input": 0.001,
				"total": 0.0123,
			},
		},
		"finish_reason": "end_turn",
		"message_id":    "msg-1",
	})

	event := lastEndpointEvent(t, logPath)
	if got := eventAction(t, event); got != "agent.response.completed" {
		t.Fatalf("event.action = %q, want agent.response.completed", got)
	}
	usage := nestedMap(t, event, "gen_ai", "usage")
	for key, want := range map[string]interface{}{
		"input_tokens":  float64(1200),
		"output_tokens": float64(340),
		"cost_usd":      0.0123,
	} {
		if usage[key] != want {
			t.Fatalf("gen_ai.usage.%s = %#v, want %#v", key, usage[key], want)
		}
	}
	if got := nestedMap(t, event, "gen_ai", "usage", "cache_read")["input_tokens"]; got != float64(900) {
		t.Fatalf("gen_ai.usage.cache_read.input_tokens = %#v, want 900", got)
	}
	if got := nestedMap(t, event, "gen_ai", "usage", "cache_creation")["input_tokens"]; got != float64(120) {
		t.Fatalf("gen_ai.usage.cache_creation.input_tokens = %#v, want 120", got)
	}
}

// Pi reports totalTokens; the schema has no total member and must not grow one. A parallel total is
// exactly the per-harness token field the schema rules forbid, and a stored total can contradict a
// stored breakdown.
func TestPiEventDropsRuntimeTotalTokens(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "message_end",
		"session_id": "pi-session-1",
		"usage": map[string]interface{}{
			"input":       float64(10),
			"output":      float64(5),
			"totalTokens": float64(15),
		},
	})

	usage := nestedMap(t, lastEndpointEvent(t, logPath), "gen_ai", "usage")
	for _, key := range []string{"total_tokens", "totalTokens", "total"} {
		if _, ok := usage[key]; ok {
			t.Fatalf("gen_ai.usage.%s was written; token usage has no total member", key)
		}
	}
}

// cost_usd carries runtime-reported cost only. Pi nests the total under usage.cost.total, and the
// per-category costs beside it have no schema member -- summing or repurposing them would be
// deriving a number Pi did not report.
func TestPiEventCostUsesRuntimeTotalOnly(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "message_end",
		"session_id": "pi-session-1",
		"usage": map[string]interface{}{
			"input":  float64(10),
			"output": float64(5),
			"cost": map[string]interface{}{
				"input":     0.002,
				"output":    0.003,
				"cacheRead": 0.001,
				"total":     0.006,
			},
		},
	})

	usage := nestedMap(t, lastEndpointEvent(t, logPath), "gen_ai", "usage")
	if usage["cost_usd"] != 0.006 {
		t.Fatalf("gen_ai.usage.cost_usd = %#v, want the runtime total 0.006", usage["cost_usd"])
	}
}

// Reasoning is its own signal with its own retention decision, so an assistant message that carried
// thinking text produces two events. Folding them together would give one event whose content
// marker describes two different pieces of text.
func TestPiEventEmitsReasoningAlongsideResponse(t *testing.T) {
	logPath := setupPiEvent(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "message_end",
		"session_id": "pi-session-1",
		"cwd":        "/tmp/project",
		"reasoning":  "First I will read the config.",
		"usage":      map[string]interface{}{"input": float64(10), "output": float64(5)},
	})

	events := endpointEvents(t, logPath)
	if len(events) != 2 {
		t.Fatalf("got %d events, want a response and a reasoning event", len(events))
	}
	actions := map[string]map[string]interface{}{}
	for _, event := range events {
		actions[eventAction(t, event)] = event
	}
	response, ok := actions["agent.response.completed"]
	if !ok {
		t.Fatalf("no agent.response.completed event: %#v", actions)
	}
	reasoning, ok := actions["agent.reasoning"]
	if !ok {
		t.Fatalf("no agent.reasoning event: %#v", actions)
	}

	parts, _ := nestedMap(t, reasoning, "gen_ai", "output")["messages"].([]interface{})
	if len(parts) == 0 {
		t.Fatalf("agent.reasoning has no gen_ai.output.messages: %#v", reasoning["gen_ai"])
	}

	// The two events must not alias each other's nested maps: the response event carries usage, the
	// reasoning event carries reasoning output, and neither may carry the other's.
	if _, ok := nestedMap(t, reasoning, "gen_ai")["usage"]; ok {
		t.Fatal("the reasoning event carried the response event's usage; the field maps are aliased")
	}
	if _, ok := nestedMap(t, response, "gen_ai")["output"]; ok {
		t.Fatal("the response event carried the reasoning event's output; the field maps are aliased")
	}
}

func TestPiEventDropsUnmappedEventTypes(t *testing.T) {
	logPath := setupPiEvent(t)

	for _, eventType := range []string{"model_select", "before_provider_request", "turn_start", ""} {
		runHookWithInput(t, runPiEvent, map[string]interface{}{
			"type":       eventType,
			"session_id": "pi-session-1",
		})
	}

	requireNoEndpointEvents(t, logPath)
}

// Pi runs this as a subprocess of an interactive session. Unparseable stdin still gets a valid empty
// reply, because a non-zero exit or a missing response makes the user's agent look broken -- a worse
// outcome than losing one event.
func TestPiEventAnswersMalformedInput(t *testing.T) {
	setupPiEvent(t)

	out := runHookWithRawInput(t, runPiEvent, "{not json")
	if out == nil {
		t.Fatal("pi-event produced no response for malformed input")
	}
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
}
