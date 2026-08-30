package cmd

import (
	"path/filepath"
	"testing"
)

// piTestLog puts a Pi extension run in a temp endpoint log and returns its path.
func piTestLog(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "pi"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	return logPath
}

func piEventActions(t *testing.T, logPath string) []string {
	t.Helper()
	var actions []string
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		actions = append(actions, meta["action"].(string))
	}
	return actions
}

func piEventWithAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			return event
		}
	}
	t.Fatalf("no %s event in log; got %v", action, piEventActions(t, logPath))
	return nil
}

// Every type the managed extension subscribes to must map to at least one event. These strings are
// the contract between the extension's subscription list and this mapper, and a typo on either side
// produces no telemetry rather than an error -- so the list is walked rather than trusted.
func TestPiEventEverySupportedTypeProducesTelemetry(t *testing.T) {
	// One payload per type, each carrying the minimum its branch needs to emit.
	payloads := map[string]map[string]interface{}{
		"session_start":    {"type": "session_start", "reason": "startup"},
		"session_shutdown": {"type": "session_shutdown", "reason": "quit"},
		"input":            {"type": "input", "text": "do the thing", "source": "interactive"},
		"tool_call":        {"type": "tool_call", "toolName": "bash", "toolCallId": "c1", "input": map[string]interface{}{"command": "ls"}},
		"tool_result":      {"type": "tool_result", "toolName": "bash", "toolCallId": "c1", "input": map[string]interface{}{"command": "ls"}},
		"user_bash":        {"type": "user_bash", "command": "git status", "cwd": "/repo"},
		"message_end": {"type": "message_end", "message": map[string]interface{}{
			"role":  "assistant",
			"usage": map[string]interface{}{"input": float64(10), "output": float64(5)},
		}},
	}

	for _, name := range supportedPiEventTypes() {
		payload, ok := payloads[name]
		if !ok {
			t.Fatalf("no fixture for supported Pi event type %q; the mapper claims to handle it", name)
		}
		events := piRuntime.endpointEvents(payload, "sess-1")
		if len(events) == 0 {
			t.Fatalf("supported Pi event type %q produced no telemetry", name)
		}
	}
}

// An unrecognized type is silent rather than generic. Pi publishes far more events than the
// extension subscribes to, and a future one becoming an undifferentiated row would fill the log
// with records no query asks for.
func TestPiEventUnknownTypeProducesNothing(t *testing.T) {
	for _, name := range []string{"message_update", "before_provider_request", "turn_start", "", "totally_new"} {
		events := piRuntime.endpointEvents(map[string]interface{}{"type": name}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("Pi event type %q produced %d events, want none", name, len(events))
		}
	}
}

func TestPiEventSessionStartAndShutdown(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "session_start", "reason": "resume", "sessionId": "sess-1", "cwd": "/repo",
	})
	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "session_shutdown", "reason": "quit", "sessionId": "sess-1",
	})

	if got := piEventActions(t, logPath); len(got) != 2 || got[0] != "session.started" || got[1] != "session.ended" {
		t.Fatalf("actions = %v, want [session.started session.ended]", got)
	}
	started := piEventWithAction(t, logPath, "session.started")
	session, _ := started["session"].(map[string]interface{})
	if session["id"] != "sess-1" {
		t.Fatalf("session.id = %v, want sess-1", session["id"])
	}
	// The reason distinguishes a fresh session from one that already has history behind it, which a
	// reader counting sessions needs.
	raw := nested(t, started, "raw")
	if raw["pi_session_reason"] != "resume" {
		t.Fatalf("raw.pi_session_reason = %v, want resume", raw["pi_session_reason"])
	}
}

func TestPiEventInputRecordsPrompt(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "input", "text": "refactor the parser", "source": "interactive",
		"sessionId": "sess-1", "cwd": "/repo",
	})

	event := piEventWithAction(t, logPath, "prompt.submitted")
	prompt := nested(t, event, "prompt")
	if prompt["text"] != "refactor the parser" {
		t.Fatalf("prompt.text = %v, want the submitted text", prompt["text"])
	}
	content := nested(t, event, "content")
	if content["included"] != true || content["hash"] == "" {
		t.Fatalf("content = %v, want retained content with a hash", content)
	}
	// "A human typed this" and "a script sent this" are different facts about the same prompt.
	if raw := nested(t, event, "raw"); raw["pi_input_source"] != "interactive" {
		t.Fatalf("raw.pi_input_source = %v, want interactive", raw["pi_input_source"])
	}
}

// An input event with no text is Pi announcing an empty submission. Recording a prompt event with
// no prompt would put a row in the log that every prompt query matches and none can explain.
func TestPiEventEmptyInputProducesNothing(t *testing.T) {
	events := piRuntime.endpointEvents(map[string]interface{}{"type": "input", "source": "interactive"}, "sess-1")
	if len(events) != 0 {
		t.Fatalf("empty input produced %d events, want none", len(events))
	}
}

func TestPiEventToolCallRecordsInvocation(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "tool_call", "toolName": "bash", "toolCallId": "call-1",
		"input": map[string]interface{}{"command": "go test ./..."}, "sessionId": "sess-1",
	})

	event := piEventWithAction(t, logPath, "tool.invoked")
	tool := nested(t, event, "tool")
	if tool["name"] != "bash" || tool["command"] != "go test ./..." {
		t.Fatalf("tool = %v, want the bash tool and its command", tool)
	}
}

// The decision this mapper must not make. Pi's tool_call handler can block a call, but that is an
// extension deciding rather than an operator being asked -- Pi exposes no approval decision through
// the extension API. An approval event here would put a decision nobody made into the log, and
// would be indistinguishable from Claude Code's PermissionRequest, which is a real one.
func TestPiEventNeverSynthesizesApprovals(t *testing.T) {
	logPath := piTestLog(t)

	for _, payload := range []map[string]interface{}{
		{"type": "tool_call", "toolName": "bash", "input": map[string]interface{}{"command": "rm -rf /tmp/x"}, "sessionId": "sess-1"},
		{"type": "tool_result", "toolName": "bash", "input": map[string]interface{}{"command": "rm -rf /tmp/x"}, "sessionId": "sess-1"},
		{"type": "user_bash", "command": "sudo -s", "sessionId": "sess-1"},
	} {
		runHookWithInput(t, runPiEvent, payload)
	}

	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		action, _ := meta["action"].(string)
		if len(action) >= 9 && action[:9] == "approval." {
			t.Fatalf("Pi produced an approval event (%s); Pi exposes no approval decision to observe", action)
		}
		if _, ok := event["approval"]; ok {
			t.Fatalf("Pi event carries an approval block: %v", event)
		}
	}
}

func TestPiEventToolResultActionsPerTool(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		args   map[string]interface{}
		action string
	}{
		{"bash", map[string]interface{}{"command": "ls"}, "command.executed"},
		{"read", map[string]interface{}{"path": "/repo/main.go"}, "file.read"},
		{"edit", map[string]interface{}{"path": "/repo/main.go"}, "file.modified"},
		{"write", map[string]interface{}{"path": "/repo/new.go"}, "file.created"},
		// grep takes a pattern, find takes a glob, ls takes a directory. None is a file operation,
		// and a custom tool's arguments mean whatever its author decided.
		{"grep", map[string]interface{}{"pattern": "TODO"}, "tool.completed"},
		{"my_custom_tool", map[string]interface{}{"anything": true}, "tool.completed"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			events := piRuntime.endpointEvents(map[string]interface{}{
				"type": "tool_result", "toolName": tc.tool, "input": tc.args,
			}, "sess-1")
			if len(events) != 1 {
				t.Fatalf("%s produced %d events, want 1", tc.tool, len(events))
			}
			if events[0].action != tc.action {
				t.Fatalf("%s action = %q, want %q", tc.tool, events[0].action, tc.action)
			}
		})
	}
}

// A file action with no file is not a file action. Pi's read tool accepts a path that failed to
// resolve, and a custom tool can share a built-in's name, so a file.read with no file field would
// produce a row every file-scoped query matches and none can explain.
func TestPiEventFileActionWithoutAPathFallsBackToTool(t *testing.T) {
	events := piRuntime.endpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "read", "input": map[string]interface{}{},
	}, "sess-1")
	if len(events) != 1 || events[0].action != "tool.completed" {
		t.Fatalf("events = %v, want a single tool.completed", events)
	}
}

// The same guard on the command side: a bash result whose arguments carried no command is a tool
// call, not a command execution, and rules/risky-command/ all match on command.command.
func TestPiEventCommandActionWithoutACommandFallsBackToTool(t *testing.T) {
	events := piRuntime.endpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "bash", "input": map[string]interface{}{},
	}, "sess-1")
	if len(events) != 1 || events[0].action != "tool.completed" {
		t.Fatalf("events = %v, want a single tool.completed", events)
	}
}

func TestPiEventEditResultRecordsTheUnifiedPatch(t *testing.T) {
	logPath := piTestLog(t)
	patch := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "edit", "sessionId": "sess-1",
		"input":   map[string]interface{}{"path": "/repo/main.go"},
		"details": map[string]interface{}{"patch": patch, "diff": "display form"},
	})

	event := piEventWithAction(t, logPath, "file.modified")
	file := nested(t, event, "file")
	if file["path"] != "/repo/main.go" || file["operation"] != "modify" {
		t.Fatalf("file = %v, want the edited path with a modify operation", file)
	}
	// The unified patch is the machine-readable one; the display diff is only a fallback.
	content := nested(t, event, "content")
	if content["bytes"] != float64(len(patch)) {
		t.Fatalf("content.bytes = %v, want the patch length %d", content["bytes"], len(patch))
	}
}

func TestPiEventFailedToolIsRecordedAsAFailure(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "bash", "sessionId": "sess-1",
		"input": map[string]interface{}{"command": "false"}, "isError": true,
	})

	event := piEventWithAction(t, logPath, "tool.failed")
	meta := nested(t, event, "event")
	if event["severity"] != "high" {
		t.Fatalf("severity = %v, want high for a failed tool", event["severity"])
	}
	if meta["category"] != "tool" {
		t.Fatalf("category = %v, want tool", meta["category"])
	}
}

func TestPiEventUserBashRecordsAnOperatorCommand(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "user_bash", "command": "git status", "cwd": "/repo",
		"excludeFromContext": true, "sessionId": "sess-1",
	})

	event := piEventWithAction(t, logPath, "command.executed")
	command := nested(t, event, "command")
	if command["command"] != "git status" {
		t.Fatalf("command.command = %v, want git status", command["command"])
	}
	// Marked as the operator's rather than silently merged in with agent-run commands: this is the
	// one command shape in Pi the agent did not originate.
	raw := nested(t, event, "raw")
	if raw["pi_user_initiated"] != true {
		t.Fatalf("raw.pi_user_initiated = %v, want true", raw["pi_user_initiated"])
	}
}

func TestPiEventMessageEndRecordsUsageAndReasoning(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "message_end", "sessionId": "sess-1",
		"message": map[string]interface{}{
			"role":  "assistant",
			"model": "claude-opus-5",
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "weighing two approaches"},
				map[string]interface{}{"type": "text", "text": "here is the answer"},
			},
			"usage": map[string]interface{}{
				"input": float64(120), "output": float64(40),
				"cacheRead": float64(90), "cacheWrite": float64(10), "reasoning": float64(25),
				"totalTokens": float64(160),
				"cost":        map[string]interface{}{"total": 0.0042},
			},
		},
	})

	reasoning := piEventWithAction(t, logPath, "agent.reasoning")
	// Only the thinking parts. The assistant's visible answer is not reasoning, and recording it as
	// such would put the model's output where a reader looking for its deliberation expects it.
	parts := nested(t, reasoning, "gen_ai", "output")
	messages, ok := parts["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("gen_ai.output.messages = %v, want one reasoning message", parts["messages"])
	}
	first, _ := messages[0].(map[string]interface{})
	firstParts, _ := first["parts"].([]interface{})
	part, _ := firstParts[0].(map[string]interface{})
	if part["type"] != "reasoning" || part["content"] != "weighing two approaches" {
		t.Fatalf("reasoning part = %v, want only the thinking text", part)
	}

	usageEvent := piEventWithAction(t, logPath, "token.usage")
	usage := nested(t, usageEvent, "gen_ai", "usage")
	if usage["input_tokens"] != float64(120) || usage["output_tokens"] != float64(40) {
		t.Fatalf("usage = %v, want the semconv token names", usage)
	}
	// Cache and reasoning are nested objects in the canonical shape, not scalars.
	if got := nested(t, usageEvent, "gen_ai", "usage", "cache_read"); got["input_tokens"] != float64(90) {
		t.Fatalf("cache_read = %v, want input_tokens 90", got)
	}
	if got := nested(t, usageEvent, "gen_ai", "usage", "cache_creation"); got["input_tokens"] != float64(10) {
		t.Fatalf("cache_creation = %v, want input_tokens 10", got)
	}
	if got := nested(t, usageEvent, "gen_ai", "usage", "reasoning"); got["output_tokens"] != float64(25) {
		t.Fatalf("reasoning = %v, want output_tokens 25", got)
	}
	if usage["cost_usd"] != 0.0042 {
		t.Fatalf("cost_usd = %v, want the runtime-reported 0.0042", usage["cost_usd"])
	}
	// Pi reports a totalTokens that can disagree with its own parts, and Beacon's usage shape has no
	// total. Carrying it would add a field nothing reads and something could contradict.
	if _, ok := usage["total_tokens"]; ok {
		t.Fatalf("usage carries total_tokens, which is not part of the canonical shape: %v", usage)
	}
	if _, ok := usage["totalTokens"]; ok {
		t.Fatalf("usage carries Pi's own totalTokens spelling: %v", usage)
	}
}

// message_end fires for user and toolResult messages too. Emitting a row for each would put an
// empty usage record in the log on every turn.
func TestPiEventMessageEndIgnoresNonAssistantMessages(t *testing.T) {
	for _, role := range []string{"user", "toolResult"} {
		events := piRuntime.endpointEvents(map[string]interface{}{
			"type":    "message_end",
			"message": map[string]interface{}{"role": role, "usage": map[string]interface{}{"input": float64(5)}},
		}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("%s message produced %d events, want none", role, len(events))
		}
	}
}

// An assistant message with neither usage nor reasoning is a message Beacon has nothing to say
// about, which is different from one it failed to read.
func TestPiEventMessageEndWithNothingToReportProducesNothing(t *testing.T) {
	events := piRuntime.endpointEvents(map[string]interface{}{
		"type":    "message_end",
		"message": map[string]interface{}{"role": "assistant", "content": []interface{}{}},
	}, "sess-1")
	if len(events) != 0 {
		t.Fatalf("bare assistant message produced %d events, want none", len(events))
	}
}

// The extension lifts Pi's cwd onto the envelope, and an event that carried its own wins. Without
// this the working directory, repository, and branch are missing from every event of a run.
func TestPiEventResolvesWorkspaceFromTheEnvelope(t *testing.T) {
	logPath := piTestLog(t)
	repo := t.TempDir()

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "input", "text": "hello", "sessionId": "sess-1", "cwd": repo,
	})

	event := piEventWithAction(t, logPath, "prompt.submitted")
	session := nested(t, event, "session")
	if session["working_directory"] != repo {
		t.Fatalf("session.working_directory = %v, want %q", session["working_directory"], repo)
	}
}

// The harness name is normalized at write time, so a Pi session is not split across two spellings
// in any query that groups by harness.name.
func TestPiEventNormalizesTheHarnessName(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "session_start", "reason": "startup", "sessionId": "sess-1",
	})

	event := piEventWithAction(t, logPath, "session.started")
	harness := nested(t, event, "harness")
	if harness["name"] != "pi_cli" {
		t.Fatalf("harness.name = %v, want pi_cli", harness["name"])
	}
}

// A payload with no session id still produces telemetry. Losing the grouping key is bad; losing the
// event is worse, and a run whose accessor failed once should not go dark.
func TestPiEventSurvivesAMissingSessionID(t *testing.T) {
	logPath := piTestLog(t)

	runHookWithInput(t, runPiEvent, map[string]interface{}{"type": "input", "text": "hello"})

	if got := piEventActions(t, logPath); len(got) != 1 || got[0] != "prompt.submitted" {
		t.Fatalf("actions = %v, want [prompt.submitted]", got)
	}
}
