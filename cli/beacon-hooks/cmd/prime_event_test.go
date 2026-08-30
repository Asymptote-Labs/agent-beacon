package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// primeTestLog puts a Prime Agent extension run in a temp endpoint log and returns its path.
func primeTestLog(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "prime"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	return logPath
}

func primeEventActions(t *testing.T, logPath string) []string {
	t.Helper()
	var actions []string
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		actions = append(actions, meta["action"].(string))
	}
	return actions
}

func primeEventWithAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			return event
		}
	}
	t.Fatalf("no %s event in log; got %v", action, primeEventActions(t, logPath))
	return nil
}

// primeMapped runs the mapper directly and returns the event with the given action.
func primeMapped(t *testing.T, payload map[string]interface{}, action string) normalizedEvent {
	t.Helper()
	var actions []string
	for _, event := range primeEndpointEvents(payload, "sess-1") {
		if event.action == action {
			return event
		}
		actions = append(actions, event.action)
	}
	t.Fatalf("no %s event; got %v", action, actions)
	return normalizedEvent{}
}

func primeCell(code string) map[string]interface{} {
	return map[string]interface{}{"code": code}
}

// Every type the managed extension subscribes to must map to at least one event. These strings are
// the contract between the extension's subscription list and this mapper, and a typo on either side
// produces no telemetry rather than an error -- so the list is walked rather than trusted.
func TestPrimeEventEverySupportedTypeProducesTelemetry(t *testing.T) {
	payloads := map[string]map[string]interface{}{
		"session_start":    {"type": "session_start", "reason": "startup"},
		"session_shutdown": {"type": "session_shutdown", "reason": "quit"},
		"input":            {"type": "input", "text": "do the thing", "source": "interactive"},
		"tool_call":        {"type": "tool_call", "toolName": "ipython", "toolCallId": "c1", "input": primeCell("print(1)")},
		"tool_result":      {"type": "tool_result", "toolName": "ipython", "toolCallId": "c1", "input": primeCell("print(1)")},
		"user_bash":        {"type": "user_bash", "command": "git status", "cwd": "/repo"},
		"message_end": {"type": "message_end", "message": map[string]interface{}{
			"role":  "assistant",
			"usage": map[string]interface{}{"input": float64(10), "output": float64(5)},
		}},
		"session_compact": {"type": "session_compact", "fromExtension": false},
		"refine_complete": {"type": "refine_complete", "id": "r1", "summary": "learned the build command",
			"appliedEdits": float64(2), "scope": "global"},
	}

	for _, name := range supportedPrimeEventTypes() {
		payload, ok := payloads[name]
		if !ok {
			t.Fatalf("no fixture for supported Prime Agent event type %q; the mapper claims to handle it", name)
		}
		if events := primeEndpointEvents(payload, "sess-1"); len(events) == 0 {
			t.Fatalf("supported Prime Agent event type %q produced no telemetry", name)
		}
	}
}

// Prime Agent's supported set is Pi's plus the two events its build fires and Pi's does not.
// Asserting the relationship rather than the literal list is what catches one runtime's list being
// edited without the other's being considered.
func TestPrimeEventTypesExtendPiEventTypes(t *testing.T) {
	prime := map[string]bool{}
	for _, name := range supportedPrimeEventTypes() {
		prime[name] = true
	}
	for _, name := range supportedPiEventTypes() {
		if !prime[name] {
			t.Fatalf("Prime Agent does not handle %q, which Pi does; both runtimes deliver the same envelope", name)
		}
	}
	for _, name := range []string{"session_compact", "refine_complete"} {
		if !prime[name] {
			t.Fatalf("Prime Agent does not handle %q; the extension subscribes to it, so those events would be dropped", name)
		}
	}
}

// An unrecognized type is silent rather than generic, for the same reason it is in the Pi mapper.
func TestPrimeEventUnknownTypeProducesNothing(t *testing.T) {
	for _, name := range []string{"message_update", "before_provider_request", "turn_start", "", "totally_new"} {
		if events := primeEndpointEvents(map[string]interface{}{"type": name}, "sess-1"); len(events) != 0 {
			t.Fatalf("Prime Agent event type %q produced %d events, want none", name, len(events))
		}
	}
}

// The whole point of giving Prime Agent its own platform: events must land under prime_agent, not
// under pi_cli, or one product's activity is filed as another's.
func TestPrimeEventWritesThePrimeAgentHarness(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "session_start", "reason": "startup", "sessionId": "sess-1", "cwd": "/repo",
	})

	event := primeEventWithAction(t, logPath, "session.started")
	harness := nested(t, event, "harness")
	if harness["name"] != "prime_agent" {
		t.Fatalf("harness.name = %v, want prime_agent", harness["name"])
	}
	// Beacon ships and versions the extension file the runtime loads, which is what "plugin" means.
	if harness["collection_method"] != "plugin" {
		t.Fatalf("harness.collection_method = %v, want plugin", harness["collection_method"])
	}
}

// Promoted raw fields are namespaced by runtime so a log carrying both products keeps them apart.
func TestPrimeEventSessionLifecycleUsesPrimeNamespacedRawFields(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "session_start", "reason": "fork", "sessionId": "sess-1", "cwd": "/repo",
	})
	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "session_shutdown", "reason": "quit", "sessionId": "sess-1",
	})

	if got := primeEventActions(t, logPath); len(got) != 2 || got[0] != "session.started" || got[1] != "session.ended" {
		t.Fatalf("actions = %v, want [session.started session.ended]", got)
	}
	started := primeEventWithAction(t, logPath, "session.started")
	if session := nested(t, started, "session"); session["id"] != "sess-1" {
		t.Fatalf("session.id = %v, want sess-1", session["id"])
	}
	raw := nested(t, started, "raw")
	if raw["prime_session_reason"] != "fork" {
		t.Fatalf("raw.prime_session_reason = %v, want fork", raw["prime_session_reason"])
	}
	if _, ok := raw["pi_session_reason"]; ok {
		t.Fatal("Prime Agent events carry Pi's raw field names; a log holding both runtimes could not tell them apart")
	}
	if _, ok := raw["prime"]; !ok {
		t.Fatalf("raw is missing the prime payload: %v", raw)
	}
}

func TestPrimeEventInputRecordsPrompt(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "input", "text": "refactor the parser", "source": "interactive",
		"sessionId": "sess-1", "cwd": "/repo",
	})

	event := primeEventWithAction(t, logPath, "prompt.submitted")
	if prompt := nested(t, event, "prompt"); prompt["text"] != "refactor the parser" {
		t.Fatalf("prompt.text = %v, want the submitted text", prompt["text"])
	}
	if content := nested(t, event, "content"); content["included"] != true || content["hash"] == "" {
		t.Fatalf("content = %v, want retained content with a hash", content)
	}
	if raw := nested(t, event, "raw"); raw["prime_input_source"] != "interactive" {
		t.Fatalf("raw.prime_input_source = %v, want interactive", raw["prime_input_source"])
	}
}

// The kernel cell is the runtime's entire execution surface, so it has to reach command.command:
// every shell command, file write and network call the agent makes is Python text inside one of
// these, and a cell recorded only as an opaque tool input matches no command-shaped detection.
func TestPrimeEventKernelCellIsRecordedAsACommand(t *testing.T) {
	code := "import subprocess\nsubprocess.run(['curl', 'https://example.test/x'])"
	event := primeMapped(t, map[string]interface{}{
		"type": "tool_call", "toolName": "ipython", "toolCallId": "c1",
		"input": primeCell(code),
	}, "tool.invoked")

	command, _ := event.fields["command"].(map[string]interface{})
	if command["command"] != code {
		t.Fatalf("command.command = %v, want the cell source", command["command"])
	}
	tool, _ := event.fields["tool"].(map[string]interface{})
	if tool["name"] != "ipython" {
		t.Fatalf("tool.name = %v, want ipython; a reader must be able to tell a Python cell from a shell command", tool["name"])
	}
	if content, _ := event.fields["content"].(map[string]interface{}); content["included"] != true {
		t.Fatalf("content = %v, want the cell source retained", event.fields["content"])
	}
}

func TestPrimeEventKernelResultRecordsOutcome(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "call-7",
		"sessionId": "sess-1", "cwd": "/repo",
		"input":   primeCell("print('hi')"),
		"isError": false,
		"details": map[string]interface{}{
			"status": "ok", "durationMs": float64(1200),
			"stdout": "hi\n", "stderr": "warning: slow\n", "result": "None",
		},
	})

	event := primeEventWithAction(t, logPath, "command.executed")
	command := nested(t, event, "command")
	if command["command"] != "print('hi')" {
		t.Fatalf("command.command = %v, want the cell source", command["command"])
	}
	if command["duration_ms"] != float64(1200) {
		t.Fatalf("command.duration_ms = %v, want 1200", command["duration_ms"])
	}
	output, _ := command["output"].(string)
	if !strings.Contains(output, "hi") || !strings.Contains(output, "warning: slow") {
		t.Fatalf("command.output = %q, want both streams the operator would have watched", output)
	}
	// The kernel reports ok/error/aborted, which is not a process exit status. Writing one would
	// put a number the runtime never produced where readers compare real exit codes.
	if _, ok := command["exit_code"]; ok {
		t.Fatalf("command.exit_code = %v, want it absent", command["exit_code"])
	}
	if raw := nested(t, event, "raw"); raw["prime_cell_status"] != "ok" {
		t.Fatalf("raw.prime_cell_status = %v, want ok", raw["prime_cell_status"])
	}
	// The join key between the cell's pre-execution row and this one.
	if call := nested(t, event, "gen_ai", "tool", "call"); call["id"] != "call-7" {
		t.Fatalf("gen_ai.tool.call.id = %v, want call-7", call["id"])
	}
}

// The kernel names the Python exception class, which is far more useful than the generic
// tool_error every runtime shares.
func TestPrimeEventKernelFailureCarriesTheExceptionClass(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input": primeCell("open('/etc/shadow')"), "isError": true,
		"details": map[string]interface{}{
			"status": "error",
			"error":  map[string]interface{}{"ename": "PermissionError", "evalue": "denied"},
		},
	}, "tool.failed")

	errFields, _ := event.fields["error"].(map[string]interface{})
	if errFields["type"] != "PermissionError" {
		t.Fatalf("error.type = %v, want PermissionError", errFields["type"])
	}
	if event.severity != "high" {
		t.Fatalf("severity = %q, want high", event.severity)
	}
}

// A cell stopped by an interrupt sets the kernel status without setting the tool protocol's own
// error flag. Reading only the flag would record a cell that never finished as one that did.
func TestPrimeEventAbortedCellIsAFailure(t *testing.T) {
	for _, status := range []string{"error", "aborted"} {
		events := primeEndpointEvents(map[string]interface{}{
			"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
			"input":   primeCell("while True: pass"),
			"details": map[string]interface{}{"status": status},
		}, "sess-1")
		if len(events) != 1 || events[0].action != "tool.failed" {
			t.Fatalf("status %q produced %v, want a single tool.failed", status, events)
		}
	}
}

// The only structured file telemetry Prime Agent produces. Its edit skill streams a payload per
// replacement, which the host collects into details.diffs.
func TestPrimeEventKernelDiffsBecomeFileEvents(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"sessionId": "sess-1", "cwd": "/repo",
		"input": primeCell("edit.run('src/app.py', 'old', 'new')"),
		"details": map[string]interface{}{
			"status": "ok",
			"diffs": []interface{}{
				map[string]interface{}{"path": "/repo/src/app.py", "oldStr": "old", "newStr": "new", "startLine": float64(12)},
			},
		},
	})

	event := primeEventWithAction(t, logPath, "file.modified")
	file := nested(t, event, "file")
	if file["path"] != "/repo/src/app.py" {
		t.Fatalf("file.path = %v, want the edited path", file["path"])
	}
	if file["operation"] != "modify" {
		t.Fatalf("file.operation = %v, want modify", file["operation"])
	}
	diff, _ := file["diff"].(string)
	if !strings.Contains(diff, "old") || !strings.Contains(diff, "new") {
		t.Fatalf("file.diff = %q, want both sides of the replacement", diff)
	}
	if file["diff_hash"] == "" || file["diff_bytes"] == nil {
		t.Fatalf("file = %v, want the diff described alongside the retained copy", file)
	}
	if file["language"] != "py" {
		t.Fatalf("file.language = %v, want py", file["language"])
	}
	// The edit is its own fact. Copying the whole Python cell onto it would repeat the cell body
	// once per edit while telling a reader nothing the cell's own row does not; the link back is
	// the tool call id, which every event from this payload carries.
	if _, ok := event["command"]; ok {
		t.Fatalf("file event carries a command block: %v", event["command"])
	}
	if call := nested(t, event, "gen_ai", "tool", "call"); call["id"] != "c1" {
		t.Fatalf("gen_ai.tool.call.id = %v, want c1", call["id"])
	}
}

// The skill emits snake_case from Python and the host converts to camelCase on the way to the
// extension. Both spellings occur, so both map.
func TestPrimeEventKernelDiffsAcceptBothKeySpellings(t *testing.T) {
	events := primeEndpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input": primeCell("edit(...)"),
		"details": map[string]interface{}{"status": "ok", "diffs": []interface{}{
			map[string]interface{}{"path": "/repo/a.py", "old_str": "before", "new_str": "after", "start_line": float64(1)},
		}},
	}, "sess-1")

	for _, event := range events {
		if event.action != "file.modified" {
			continue
		}
		file, _ := event.fields["file"].(map[string]interface{})
		diff, _ := file["diff"].(string)
		if !strings.Contains(diff, "before") || !strings.Contains(diff, "after") {
			t.Fatalf("file.diff = %q, want the snake_case payload mapped", diff)
		}
		return
	}
	t.Fatalf("no file.modified event for a snake_case diff payload; got %v", events)
}

// The kernel streams edits as the cell runs, so an exception on the last line does not un-write the
// file the third line wrote. Dropping the edits on failure would lose changes that are on disk.
func TestPrimeEventKernelDiffsSurviveAFailedCell(t *testing.T) {
	events := primeEndpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input": primeCell("edit(...)\nraise SystemExit"), "isError": true,
		"details": map[string]interface{}{"status": "error", "errorEname": "SystemExit", "diffs": []interface{}{
			map[string]interface{}{"path": "/repo/a.py", "oldStr": "x", "newStr": "y"},
		}},
	}, "sess-1")

	var actions []string
	for _, event := range events {
		actions = append(actions, event.action)
	}
	if len(actions) != 2 || actions[0] != "tool.failed" || actions[1] != "file.modified" {
		t.Fatalf("actions = %v, want [tool.failed file.modified]", actions)
	}
}

// A diff entry with no path names no file, so it produces no file event rather than one with an
// empty path that every file-scoped query matches and none can explain.
func TestPrimeEventKernelDiffsWithoutAPathAreDropped(t *testing.T) {
	events := primeEndpointEvents(map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input": primeCell("edit(...)"),
		"details": map[string]interface{}{"status": "ok", "diffs": []interface{}{
			map[string]interface{}{"oldStr": "x", "newStr": "y"},
			"not-an-object",
		}},
	}, "sess-1")

	for _, event := range events {
		if event.action == "file.modified" {
			t.Fatalf("a diff with no path produced a file event: %v", event.fields)
		}
	}
}

// One agent directing another is a different fact from an assistant replying to its user, so it
// gets its own action rather than reusing agent.message.
func TestPrimeEventAgentMessagesAreRecorded(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"sessionId": "sess-1", "cwd": "/repo",
		"input": primeCell("agent_message.send(...)"),
		"details": map[string]interface{}{"status": "ok", "sentAgentMessages": []interface{}{
			map[string]interface{}{
				"id": "m-1", "message": "deploy the staging branch", "deliveryStatus": "delivered",
				"receiverRole": "child",
				"target":       map[string]interface{}{"sessionId": "sess-2", "activeSessionId": "sess-2", "sessionName": "builder"},
			},
		}},
	})

	event := primeEventWithAction(t, logPath, "agent.message.sent")
	raw := nested(t, event, "raw")
	for key, want := range map[string]interface{}{
		"prime_agent_message_text":           "deploy the staging branch",
		"prime_agent_message_target_session": "sess-2",
		"prime_agent_message_target_name":    "builder",
		"prime_agent_message_receiver_role":  "child",
		"prime_agent_message_delivery":       "delivered",
		"prime_agent_message_id":             "m-1",
	} {
		if raw[key] != want {
			t.Fatalf("raw.%s = %v, want %v", key, raw[key], want)
		}
	}
	if content := nested(t, event, "content"); content["included"] != true {
		t.Fatalf("content = %v, want the message body retained", content)
	}
}

// Without this row the prompts and tool results before a compaction read as though they are still
// in context when they are not.
func TestPrimeEventCompactionIsRecorded(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "session_compact", "fromExtension": true,
		"compactionEntry": map[string]interface{}{"id": "c-1"},
	}, "session.compacted")

	if event.category != "session" {
		t.Fatalf("category = %q, want session", event.category)
	}
	raw, _ := event.fields["raw"].(map[string]interface{})
	if raw["prime_compaction_from_extension"] != true {
		t.Fatalf("raw.prime_compaction_from_extension = %v, want true", raw["prime_compaction_from_extension"])
	}
}

// A refinement rewrites the durable prompts, memories and skills the agent starts every future
// session from. It is the one action in the runtime that outlives its own session.
func TestPrimeEventHarnessRefinementIsRecorded(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "refine_complete", "id": "r-1", "summary": "always run make lint before committing",
		"appliedEdits": float64(3), "scope": "global",
		"sessionId": "sess-1", "cwd": "/repo",
	})

	event := primeEventWithAction(t, logPath, "agent.harness.refined")
	raw := nested(t, event, "raw")
	if raw["prime_refinement_summary"] != "always run make lint before committing" {
		t.Fatalf("raw.prime_refinement_summary = %v, want the summary promoted", raw["prime_refinement_summary"])
	}
	if raw["prime_refinement_scope"] != "global" || raw["prime_refinement_applied_edits"] != float64(3) {
		t.Fatalf("raw = %v, want scope and edit count promoted", raw)
	}
	if content := nested(t, event, "content"); content["included"] != true {
		t.Fatalf("content = %v, want the summary retained", content)
	}
	if meta := nested(t, event, "event"); meta["category"] != "session" {
		t.Fatalf("event.category = %v, want session", meta["category"])
	}
}

// Severity separates the durable change from the one that dies with its session. Nothing is
// blocked or judged here; this is what lets a rule or a dashboard sort them.
func TestPrimeEventHarnessRefinementSeverityFollowsScope(t *testing.T) {
	for _, tc := range []struct {
		name         string
		scope        string
		appliedEdits interface{}
		want         string
	}{
		{"global refinement persists into every future session", "global", float64(2), "medium"},
		{"local refinement dies with the session", "local", float64(2), "info"},
		{"a global round that changed nothing changed nothing", "global", float64(0), "info"},
		{"an unreported edit count is not evidence of no change", "global", nil, "medium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]interface{}{"type": "refine_complete", "scope": tc.scope, "summary": "s"}
			if tc.appliedEdits != nil {
				payload["appliedEdits"] = tc.appliedEdits
			}
			event := primeMapped(t, payload, "agent.harness.refined")
			if event.severity != tc.want {
				t.Fatalf("severity = %q, want %q", event.severity, tc.want)
			}
		})
	}
}

// A tool another extension registered has arguments that mean whatever its author decided, so it is
// recorded as tool activity without a command or file block invented for it.
func TestPrimeEventCustomToolInventsNothing(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "tool_result", "toolName": "lookup_ticket", "toolCallId": "c1",
		"input": map[string]interface{}{"ticket": "ABC-1"},
	}, "tool.completed")

	for _, key := range []string{"command", "file"} {
		if _, ok := event.fields[key]; ok {
			t.Fatalf("custom tool produced a %s block: %v", key, event.fields[key])
		}
	}
	if tool, _ := event.fields["tool"].(map[string]interface{}); tool["name"] != "lookup_ticket" {
		t.Fatalf("tool.name = %v, want the tool's own name", tool["name"])
	}
}

// A cell with no code is not a command. Reporting command.executed with nothing in it would produce
// a row every command-scoped query matches and none can explain.
func TestPrimeEventEmptyCellIsNotACommand(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input": map[string]interface{}{}, "details": map[string]interface{}{"status": "ok"},
	}, "tool.completed")

	if _, ok := event.fields["command"]; ok {
		t.Fatalf("an empty cell produced a command block: %v", event.fields["command"])
	}
}

// The kernel restart notice matters to a reader reconstructing what the agent was working from:
// nothing it set up earlier in the session survived into this cell.
func TestPrimeEventKernelRestartIsRecorded(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
		"input":   primeCell("print(x)"),
		"details": map[string]interface{}{"status": "ok", "kernelRestarted": true},
	}, "command.executed")

	raw, _ := event.fields["raw"].(map[string]interface{})
	if raw["prime_kernel_restarted"] != true {
		t.Fatalf("raw.prime_kernel_restarted = %v, want true", raw["prime_kernel_restarted"])
	}
}

// The shared envelope mapping has to work for Prime Agent too, not only for Pi: usage and reasoning
// are reported in the same place by both.
func TestPrimeEventMessageEndRecordsUsageAndReasoning(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "message_end", "sessionId": "sess-1",
		"message": map[string]interface{}{
			"role":  "assistant",
			"model": "anthropic/claude-opus-5",
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "check the tests first"},
				map[string]interface{}{"type": "text", "text": "Done."},
			},
			"usage": map[string]interface{}{
				"input": float64(100), "output": float64(20), "reasoning": float64(8),
				"cacheRead": float64(5), "cacheWrite": float64(2),
				"cost": map[string]interface{}{"total": 0.0123},
			},
		},
	})

	usage := nested(t, primeEventWithAction(t, logPath, "token.usage"), "gen_ai", "usage")
	if usage["input_tokens"] != float64(100) || usage["output_tokens"] != float64(20) {
		t.Fatalf("gen_ai.usage = %v, want the runtime's own counts", usage)
	}
	if usage["cost_usd"] != 0.0123 {
		t.Fatalf("gen_ai.usage.cost_usd = %v, want the runtime-reported cost", usage["cost_usd"])
	}
	reasoning := primeEventWithAction(t, logPath, "agent.reasoning")
	parts := nested(t, reasoning, "gen_ai", "output")
	messages, ok := parts["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("gen_ai.output.messages = %v, want one reasoning message", parts["messages"])
	}
	first, _ := messages[0].(map[string]interface{})
	fragments, _ := first["parts"].([]interface{})
	if len(fragments) != 1 {
		t.Fatalf("reasoning parts = %v, want exactly the thinking part", first["parts"])
	}
	part, _ := fragments[0].(map[string]interface{})
	if part["content"] != "check the tests first" {
		t.Fatalf("reasoning content = %v, want the model's thinking", part["content"])
	}
	// The assistant's visible answer is not reasoning; recording it as such would put the model's
	// output where a reader looking for its private deliberation expects to find it.
	if text, _ := part["content"].(string); strings.Contains(text, "Done.") {
		t.Fatalf("reasoning event carried the assistant's answer: %q", text)
	}
}

// The user's own `!` command is the one command shape in the runtime the agent did not originate.
func TestPrimeEventUserBashIsMarkedUserInitiated(t *testing.T) {
	event := primeMapped(t, map[string]interface{}{
		"type": "user_bash", "command": "git status", "cwd": "/repo", "excludeFromContext": true,
	}, "command.executed")

	raw, _ := event.fields["raw"].(map[string]interface{})
	if raw["prime_user_initiated"] != true || raw["prime_exclude_from_context"] != true {
		t.Fatalf("raw = %v, want the operator's command marked as theirs", raw)
	}
}

// The extension lifts the session id onto the envelope as `sessionId`; nothing in this runtime
// sends `session_id`. A mapper that fell through to the default reader would still write every
// event, and every one of them would lose session.id -- the field that groups a run -- so the log
// would look healthy while nothing in it could be tied together.
//
// Asserted across every event this mapper produces rather than on one, because the loss is silent
// and per-event.
func TestPrimeEventCarriesTheSessionIdOnEveryEvent(t *testing.T) {
	payloads := []map[string]interface{}{
		{"type": "session_start", "reason": "startup", "sessionId": "sess-9"},
		{"type": "input", "text": "do it", "sessionId": "sess-9"},
		{"type": "tool_result", "toolName": "ipython", "toolCallId": "c1", "sessionId": "sess-9",
			"input": primeCell("print(1)"),
			"details": map[string]interface{}{"status": "ok", "diffs": []interface{}{
				map[string]interface{}{"path": "/repo/a.py", "oldStr": "x", "newStr": "y"},
			}, "sentAgentMessages": []interface{}{
				map[string]interface{}{"id": "m1", "message": "go", "target": map[string]interface{}{"sessionId": "sess-8"}},
			}},
		},
		{"type": "refine_complete", "id": "r1", "summary": "s", "scope": "local", "sessionId": "sess-9"},
	}

	logPath := primeTestLog(t)
	for _, payload := range payloads {
		runHookWithInput(t, runPrimeEvent, payload)
	}

	events := endpointEvents(t, logPath)
	if len(events) < 6 {
		t.Fatalf("wrote %d events, want one per mapped action", len(events))
	}
	for _, event := range events {
		meta, _ := event["event"].(map[string]interface{})
		session, _ := event["session"].(map[string]interface{})
		if session["id"] != "sess-9" {
			t.Fatalf("%v event session.id = %v, want sess-9", meta["action"], session["id"])
		}
	}
}
