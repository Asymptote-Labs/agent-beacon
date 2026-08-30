package cmd

import (
	"path/filepath"
	"reflect"
	"testing"
)

// ompTestLog puts an Oh My Pi extension run in a temp endpoint log and returns its path.
func ompTestLog(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "omp"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	return logPath
}

func ompEventActions(t *testing.T, logPath string) []string {
	t.Helper()
	var actions []string
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		actions = append(actions, meta["action"].(string))
	}
	return actions
}

func ompEventWithAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			return event
		}
	}
	t.Fatalf("no %s event in log; got %v", action, ompEventActions(t, logPath))
	return nil
}

// ompPayloads returns one payload per supported event type, each carrying the minimum its branch
// needs to emit. Shared by the contract test and the Pi/Oh My Pi separation test.
func ompPayloads() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
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
}

// Every type the managed extension subscribes to must map to at least one event. These strings are
// the contract between the extension's subscription list and this mapper, and a typo on either side
// produces no telemetry rather than an error -- so the list is walked rather than trusted.
func TestOmpEventEverySupportedTypeProducesTelemetry(t *testing.T) {
	payloads := ompPayloads()
	for _, name := range supportedOmpEventTypes() {
		payload, ok := payloads[name]
		if !ok {
			t.Fatalf("no fixture for supported Oh My Pi event type %q; the mapper claims to handle it", name)
		}
		events := ompRuntime.endpointEvents(payload, "sess-1")
		if len(events) == 0 {
			t.Fatalf("supported Oh My Pi event type %q produced no telemetry", name)
		}
	}
}

// An unrecognized type is silent rather than generic. Oh My Pi publishes far more than the
// extension subscribes to -- provider request/response internals, streaming message updates,
// compaction and retry signals -- and a future one becoming an undifferentiated row would fill the
// log with records no investigation asks for.
func TestOmpEventUnknownTypeProducesNothing(t *testing.T) {
	for _, name := range []string{
		"message_update", "before_provider_request", "after_provider_response", "turn_start",
		"auto_compaction_start", "ttsr_triggered", "", "totally_new",
	} {
		events := ompRuntime.endpointEvents(map[string]interface{}{"type": name}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("Oh My Pi event type %q produced %d events, want none", name, len(events))
		}
	}
}

// Oh My Pi and Pi are separately installed products that a fleet can run side by side. Sharing the
// mapper must not mean sharing identity: every event has to name the runtime that produced it, in
// the harness name, in the message, and in the `raw` block an operator reads to see the payload.
//
// This is the test that fails if the shared core in pi_family.go ever starts hardcoding one
// runtime's name -- which is the way a refactor like that goes wrong silently.
func TestOmpEventIsAttributedToOhMyPiNotPi(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "session_start", "reason": "startup", "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "session.started")

	// The harness name is normalized at write time, so an Oh My Pi session is not split across two
	// spellings in any query that groups by harness.name -- and is never merged into Pi's.
	harness := nested(t, event, "harness")
	if harness["name"] != "omp" {
		t.Fatalf("harness.name = %v, want omp", harness["name"])
	}

	if event["message"] != "Oh My Pi session started" {
		t.Fatalf("message = %v, want it to name Oh My Pi", event["message"])
	}

	raw := nested(t, event, "raw")
	if _, ok := raw["omp"]; !ok {
		t.Fatalf("raw = %v, want the verbatim payload under an omp key", raw)
	}
	if _, ok := raw["pi"]; ok {
		t.Fatalf("raw = %v, want no pi key on an Oh My Pi event", raw)
	}
	if raw["omp_session_reason"] != "startup" {
		t.Fatalf("raw.omp_session_reason = %v, want startup", raw["omp_session_reason"])
	}
}

// The converse, on the same shared mapper: a Pi payload must keep naming Pi. Together with the test
// above, this pins the property that matters -- one shared shape, two identities -- rather than
// just asserting each side in isolation.
func TestPiAndOmpProduceTheSameShapeUnderDifferentIdentities(t *testing.T) {
	for name, payload := range ompPayloads() {
		t.Run(name, func(t *testing.T) {
			piEvents := piRuntime.endpointEvents(cloneFields(payload), "sess-1")
			ompEvents := ompRuntime.endpointEvents(cloneFields(payload), "sess-1")

			if len(piEvents) != len(ompEvents) {
				t.Fatalf("pi produced %d events and omp %d for %q; the shared mapper should emit "+
					"the same events for the same payload", len(piEvents), len(ompEvents), name)
			}
			for i := range piEvents {
				if piEvents[i].action != ompEvents[i].action {
					t.Fatalf("action[%d] = %q (pi) vs %q (omp)", i, piEvents[i].action, ompEvents[i].action)
				}
				if piEvents[i].category != ompEvents[i].category || piEvents[i].severity != ompEvents[i].severity {
					t.Fatalf("event[%d] category/severity differ between runtimes: %+v vs %+v",
						i, piEvents[i], ompEvents[i])
				}
				if piEvents[i].message == ompEvents[i].message {
					t.Fatalf("event[%d] message %q is identical for both runtimes; a reader could "+
						"not tell which one produced it", i, piEvents[i].message)
				}
				if _, ok := piEvents[i].fields["raw"].(map[string]interface{})["pi"]; !ok {
					t.Fatalf("pi event[%d] raw block is not keyed by pi: %v", i, piEvents[i].fields["raw"])
				}
				if _, ok := ompEvents[i].fields["raw"].(map[string]interface{})["omp"]; !ok {
					t.Fatalf("omp event[%d] raw block is not keyed by omp: %v", i, ompEvents[i].fields["raw"])
				}
			}
		})
	}
}

// Beacon ships and versions the extension file Oh My Pi loads, so its events are plugin-collected,
// not hook-collected. `event.fidelity` stays observed because every action here was named by the
// runtime rather than derived by Beacon.
func TestOmpEventCarriesPluginProvenance(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "input", "text": "hello", "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "prompt.submitted")
	if method := nested(t, event, "harness")["collection_method"]; method != "plugin" {
		t.Fatalf("harness.collection_method = %v, want plugin", method)
	}
	if fidelity := nested(t, event, "event")["fidelity"]; fidelity != "observed" {
		t.Fatalf("event.fidelity = %v, want observed", fidelity)
	}
}

func TestOmpEventPromptRecordsTextAndSource(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "input", "text": "refactor the parser", "source": "interactive", "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "prompt.submitted")
	if prompt := nested(t, event, "prompt"); prompt["text"] != "refactor the parser" {
		t.Fatalf("prompt.text = %v, want the submitted text", prompt["text"])
	}
	if content := nested(t, event, "content"); content["included"] != true || content["hash"] == "" {
		t.Fatalf("content = %v, want retained content with a hash", content)
	}
	// "A human typed this" and "a script sent this" are different facts about the same prompt.
	if raw := nested(t, event, "raw"); raw["omp_input_source"] != "interactive" {
		t.Fatalf("raw.omp_input_source = %v, want interactive", raw["omp_input_source"])
	}
}

// A tool_call is not an approval. Oh My Pi's tool_call handler can block a call, but that is an
// extension deciding rather than an operator being asked; the runtime's real operator decisions
// arrive as their own approval events. Recording a block as an approval would be indistinguishable
// from a decision a human actually made.
func TestOmpEventToolCallIsNotAnApproval(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_call", "toolName": "bash", "toolCallId": "call-1",
		"input": map[string]interface{}{"command": "rm -rf /tmp/x"}, "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "tool.invoked")
	if tool := nested(t, event, "tool"); tool["name"] != "bash" || tool["command"] != "rm -rf /tmp/x" {
		t.Fatalf("tool = %v, want the bash tool and its command", tool)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("tool.invoked carries an approval block: %v", event)
	}
}

func TestOmpEventToolResultActionsPerTool(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		args   map[string]interface{}
		action string
	}{
		{"bash", map[string]interface{}{"command": "ls"}, "command.executed"},
		{"read", map[string]interface{}{"path": "/repo/main.go"}, "file.read"},
		{"edit", map[string]interface{}{"path": "/repo/main.go"}, "file.modified"},
		{"write", map[string]interface{}{"path": "/repo/new.go"}, "file.created"},
		// grep takes a pattern and glob takes a glob. Neither is a file operation, and a custom
		// tool registered by another extension means whatever its author decided.
		{"grep", map[string]interface{}{"pattern": "TODO"}, "tool.completed"},
		{"glob", map[string]interface{}{"pattern": "**/*.go"}, "tool.completed"},
		{"my_custom_tool", map[string]interface{}{"anything": true}, "tool.completed"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			events := ompRuntime.endpointEvents(map[string]interface{}{
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

func TestOmpEventFailedToolIsRecordedAsAFailure(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "bash", "toolCallId": "call-1",
		"input": map[string]interface{}{"command": "false"}, "isError": true, "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "tool.failed")
	if event["severity"] != "high" {
		t.Fatalf("severity = %v, want high for a failed tool", event["severity"])
	}
	if meta := nested(t, event, "event"); meta["category"] != "tool" {
		t.Fatalf("category = %v, want tool", meta["category"])
	}
	if errBlock := nested(t, event, "error"); errBlock["type"] != "tool_error" {
		t.Fatalf("error = %v, want a tool_error", errBlock)
	}
}

func TestOmpEventEditResultRecordsTheUnifiedPatch(t *testing.T) {
	logPath := ompTestLog(t)
	patch := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "edit", "toolCallId": "call-1",
		"input":     map[string]interface{}{"path": "/repo/main.go"},
		"details":   map[string]interface{}{"patch": patch, "diff": "display-only"},
		"sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "file.modified")
	file := nested(t, event, "file")
	if file["path"] != "/repo/main.go" || file["operation"] != "modify" || file["language"] != "go" {
		t.Fatalf("file = %v, want the edited Go file", file)
	}
	// The unified patch is the machine-readable form; the display `diff` is only a fallback, and
	// it is shorter -- so the retained byte count is what distinguishes which one was recorded.
	if content := nested(t, event, "content"); content["bytes"] != float64(len(patch)) {
		t.Fatalf("content.bytes = %v, want the unified patch length %d", content["bytes"], len(patch))
	}
}

// A command the operator ran with the `!` prefix is the one command shape here the agent did not
// originate, so it is marked rather than merged in with agent-run commands.
func TestOmpEventUserBashIsMarkedOperatorInitiated(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "user_bash", "command": "git status", "excludeFromContext": true,
		"cwd": "/repo", "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "command.executed")
	if command := nested(t, event, "command"); command["command"] != "git status" {
		t.Fatalf("command = %v, want git status", command)
	}
	raw := nested(t, event, "raw")
	if raw["omp_user_initiated"] != true {
		t.Fatalf("raw.omp_user_initiated = %v, want true", raw["omp_user_initiated"])
	}
	if raw["omp_exclude_from_context"] != true {
		t.Fatalf("raw.omp_exclude_from_context = %v, want true", raw["omp_exclude_from_context"])
	}
}

// Oh My Pi reports token usage the same way Pi does, and `gen_ai.usage` stays the only place
// Beacon records it. `output` already includes `reasoning`, so reasoning gets its own key and is
// added to nothing; `totalTokens` is dropped rather than written as a field that can disagree with
// its own parts.
func TestOmpEventNormalizesTokenUsage(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "message_end", "sessionId": "sess-1",
		"message": map[string]interface{}{
			"role":  "assistant",
			"model": "anthropic/claude-opus-4",
			"usage": map[string]interface{}{
				"input": float64(120), "output": float64(45),
				"cacheRead": float64(900), "cacheWrite": float64(30),
				"reasoning": float64(12), "totalTokens": float64(1095),
				"cost": map[string]interface{}{"total": 0.0123},
			},
		},
	})

	event := ompEventWithAction(t, logPath, "token.usage")
	usage := nested(t, nested(t, event, "gen_ai"), "usage")
	for key, want := range map[string]float64{
		"input_tokens": 120, "output_tokens": 45, "cost_usd": 0.0123,
	} {
		if got, _ := usage[key].(float64); got != want {
			t.Fatalf("gen_ai.usage.%s = %v, want %v", key, usage[key], want)
		}
	}
	if got := nested(t, usage, "cache_read")["input_tokens"]; got != float64(900) {
		t.Fatalf("cache_read.input_tokens = %v, want 900", got)
	}
	if got := nested(t, usage, "cache_creation")["input_tokens"]; got != float64(30) {
		t.Fatalf("cache_creation.input_tokens = %v, want 30", got)
	}
	if got := nested(t, usage, "reasoning")["output_tokens"]; got != float64(12) {
		t.Fatalf("reasoning.output_tokens = %v, want 12", got)
	}
	if _, ok := usage["total_tokens"]; ok {
		t.Fatalf("gen_ai.usage carries a total: %v", usage)
	}
	if model := event["model"]; model != "anthropic/claude-opus-4" {
		t.Fatalf("model = %v, want the responding model", model)
	}
}

// Only the thinking parts are reasoning. The assistant's visible answer is not, and recording it as
// such would put the model's output where a reader looking for its private deliberation expects to
// find it.
func TestOmpEventRecordsOnlyThinkingPartsAsReasoning(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "message_end", "sessionId": "sess-1",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "weighing two approaches"},
				map[string]interface{}{"type": "text", "text": "I'll use the second one."},
			},
		},
	})

	event := ompEventWithAction(t, logPath, "agent.reasoning")
	messages, ok := nested(t, nested(t, event, "gen_ai"), "output")["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("gen_ai.output.messages = %v, want one message", messages)
	}
	parts, _ := messages[0].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("parts = %v, want one reasoning part", parts)
	}
	part, _ := parts[0].(map[string]interface{})
	if part["type"] != "reasoning" || part["content"] != "weighing two approaches" {
		t.Fatalf("part = %v, want only the thinking text as a reasoning part", part)
	}
	// The retained content is the thinking text alone, not the thinking plus the visible answer.
	thinking := "weighing two approaches"
	if content := nested(t, event, "content"); content["bytes"] != float64(len(thinking)) {
		t.Fatalf("content.bytes = %v, want just the thinking text (%d bytes); the assistant's "+
			"visible answer must not be recorded as reasoning", content["bytes"], len(thinking))
	}
}

// message_end fires for user and toolResult messages too. A message with neither usage nor
// reasoning must produce nothing rather than an empty row per turn.
func TestOmpEventBareMessageEndProducesNothing(t *testing.T) {
	for _, message := range []map[string]interface{}{
		{"role": "user", "content": []interface{}{}},
		{"role": "assistant", "content": []interface{}{}},
		{"role": "toolResult"},
	} {
		events := ompRuntime.endpointEvents(map[string]interface{}{
			"type": "message_end", "message": message,
		}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("message_end for %v produced %d events, want none", message, len(events))
		}
	}
}

// The extension lifts the runtime's cwd onto the envelope; an event carrying its own cwd wins.
func TestOmpEventResolvesWorkspaceFromTheEnvelope(t *testing.T) {
	logPath := ompTestLog(t)
	repo := t.TempDir()

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "input", "text": "hello", "sessionId": "sess-1", "cwd": repo,
	})

	event := ompEventWithAction(t, logPath, "prompt.submitted")
	if session := nested(t, event, "session"); session["working_directory"] != repo {
		t.Fatalf("session.working_directory = %v, want %q", session["working_directory"], repo)
	}
}

func TestOmpEventGroupsBySessionID(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "input", "text": "hello", "sessionId": "sess-42",
	})

	event := ompEventWithAction(t, logPath, "prompt.submitted")
	if session := nested(t, event, "session"); session["id"] != "sess-42" {
		t.Fatalf("session.id = %v, want sess-42", session["id"])
	}
}

// A payload with no session id still produces telemetry. Losing the grouping key is bad; losing the
// event is worse, and a run whose accessor failed once should not go dark.
func TestOmpEventSurvivesAMissingSessionID(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{"type": "input", "text": "hello"})

	if got := ompEventActions(t, logPath); !reflect.DeepEqual(got, []string{"prompt.submitted"}) {
		t.Fatalf("actions = %v, want [prompt.submitted]", got)
	}
}
