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

// primePayloads returns one payload per supported event type, each carrying the minimum its branch
// needs to emit. The tool fixtures use `ipython` because that is Prime Agent's only default tool;
// `bash` and `edit` exist but are opt-in, and a fixture set built from them would pass while the
// tool the runtime actually calls went unmapped.
func primePayloads() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"session_start":    {"type": "session_start", "reason": "startup"},
		"session_shutdown": {"type": "session_shutdown", "reason": "quit"},
		"input":            {"type": "input", "text": "do the thing", "source": "interactive"},
		"tool_call": {"type": "tool_call", "toolName": "ipython", "toolCallId": "c1",
			"input": map[string]interface{}{"code": "print(1)"}},
		"tool_result": {"type": "tool_result", "toolName": "ipython", "toolCallId": "c1",
			"input":   map[string]interface{}{"code": "print(1)"},
			"details": map[string]interface{}{"status": "ok", "stdout": "1\n"}},
		"user_bash": {"type": "user_bash", "command": "git status", "cwd": "/repo"},
		"message_end": {"type": "message_end", "message": map[string]interface{}{
			"role":  "assistant",
			"usage": map[string]interface{}{"input": float64(10), "output": float64(5)},
		}},
	}
}

// Every type the managed extension subscribes to must map to at least one event. These strings are
// the contract between the extension's subscription list and this mapper, and a typo on either side
// produces no telemetry rather than an error -- so the list is walked rather than trusted.
func TestPrimeEventEverySupportedTypeProducesTelemetry(t *testing.T) {
	payloads := primePayloads()
	for _, name := range supportedPrimeEventTypes() {
		payload, ok := payloads[name]
		if !ok {
			t.Fatalf("no fixture for supported Prime Agent event type %q; the mapper claims to handle it", name)
		}
		if events := primeRuntime.endpointEvents(cloneFields(payload), "sess-1"); len(events) == 0 {
			t.Fatalf("supported Prime Agent event type %q produced no telemetry", name)
		}
	}
}

// An unrecognized type is silent rather than generic. Prime Agent publishes upwards of forty event
// types -- provider request and response internals, streaming message updates, compaction,
// refinement and tree navigation signals, TUI plumbing -- and a future one becoming an
// undifferentiated row would fill the log with records no investigation asks for.
func TestPrimeEventUnknownTypeProducesNothing(t *testing.T) {
	for _, name := range []string{
		"message_update", "before_provider_request", "after_provider_response", "turn_start",
		"session_before_refine", "refine_complete", "resources_discover", "thinking_level_select",
		"", "totally_new",
	} {
		events := primeRuntime.endpointEvents(map[string]interface{}{"type": name}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("Prime Agent event type %q produced %d events, want none", name, len(events))
		}
	}
}

// Prime Agent, Pi and Oh My Pi are separately installed products that one machine can run side by
// side. Sharing the mapper must not mean sharing identity: every event has to name the runtime that
// produced it, in the harness name, in the message, and in the `raw` block an operator reads.
func TestPrimeEventIsAttributedToPrimeAgentNotPi(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "session_start", "reason": "startup", "sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "session.started")

	// The `--platform` is `prime`; the harness name events are written under is `prime_agent`.
	// Normalizing at write time is what keeps one Prime Agent session from being split across two
	// spellings in any query that groups by harness.name -- and from being merged into Pi's.
	harness := nested(t, event, "harness")
	if harness["name"] != "prime_agent" {
		t.Fatalf("harness.name = %v, want prime_agent", harness["name"])
	}

	if event["message"] != "Prime Agent session started" {
		t.Fatalf("message = %v, want it to name Prime Agent", event["message"])
	}

	raw := nested(t, event, "raw")
	if _, ok := raw["prime"]; !ok {
		t.Fatalf("raw = %v, want the verbatim payload under a prime key", raw)
	}
	for _, other := range []string{"pi", "omp"} {
		if _, ok := raw[other]; ok {
			t.Fatalf("raw = %v, want no %s key on a Prime Agent event", raw, other)
		}
	}
	if raw["prime_session_reason"] != "startup" {
		t.Fatalf("raw.prime_session_reason = %v, want startup", raw["prime_session_reason"])
	}
}

// One shared shape, three identities. The shared core in pi_family.go must never start hardcoding
// one runtime's name -- which is the way a refactor like that goes wrong silently.
func TestPiAndPrimeProduceTheSameShapeUnderDifferentIdentities(t *testing.T) {
	shared := map[string]bool{}
	for _, name := range supportedPiEventTypes() {
		shared[name] = true
	}
	for name, payload := range primePayloads() {
		if !shared[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			piEvents := piRuntime.endpointEvents(cloneFields(payload), "sess-1")
			primeEvents := primeRuntime.endpointEvents(cloneFields(payload), "sess-1")

			if len(piEvents) != len(primeEvents) {
				t.Fatalf("pi produced %d events and prime %d for %q; the shared mapper should emit "+
					"the same events for the same payload", len(piEvents), len(primeEvents), name)
			}
			for i := range piEvents {
				if piEvents[i].action != primeEvents[i].action {
					t.Fatalf("action[%d] = %q (pi) vs %q (prime)", i, piEvents[i].action, primeEvents[i].action)
				}
				if piEvents[i].message == primeEvents[i].message {
					t.Fatalf("event[%d] message %q is identical for both runtimes; a reader could "+
						"not tell which one produced it", i, piEvents[i].message)
				}
				if _, ok := primeEvents[i].fields["raw"].(map[string]interface{})["prime"]; !ok {
					t.Fatalf("prime event[%d] raw block is not keyed by prime: %v", i, primeEvents[i].fields["raw"])
				}
			}
		})
	}
}

// Beacon ships and versions the extension file Prime Agent loads, so its events are
// plugin-collected, not hook-collected. `event.fidelity` stays observed because every action here
// was named by the runtime rather than derived by Beacon.
func TestPrimeEventCarriesPluginProvenance(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "input", "text": "hello", "sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "prompt.submitted")
	if method := nested(t, event, "harness")["collection_method"]; method != "plugin" {
		t.Fatalf("harness.collection_method = %v, want plugin", method)
	}
	if fidelity := nested(t, event, "event")["fidelity"]; fidelity != "observed" {
		t.Fatalf("event.fidelity = %v, want observed", fidelity)
	}
}

// The ipython cell is the whole of Prime Agent's default tool surface: the agent reads files,
// writes files and runs shell commands through it rather than through separate tools. Recording it
// as an anonymous `tool.completed` would leave every one of those actions invisible to the
// command-scoped rules, which all match on `command.command`.
func TestPrimeEventIpythonIsRecordedAsACommand(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "call-1",
		"input": map[string]interface{}{"code": "import os; os.system('rm -rf /tmp/x')"},
		"details": map[string]interface{}{
			"status": "ok", "durationMs": float64(42), "stdout": "done\n",
		},
		"sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "command.executed")
	command := nested(t, event, "command")
	if command["command"] != "import os; os.system('rm -rf /tmp/x')" {
		t.Fatalf("command.command = %v, want the cell's code", command["command"])
	}
	if command["output"] != "done\n" {
		t.Fatalf("command.output = %v, want the captured stdout", command["output"])
	}
	if command["duration_ms"] != float64(42) {
		t.Fatalf("command.duration_ms = %v, want 42", command["duration_ms"])
	}
	if tool := nested(t, event, "tool"); tool["name"] != "ipython" {
		t.Fatalf("tool.name = %v, want ipython", tool["name"])
	}
	raw := nested(t, event, "raw")
	// Marked as Python rather than passed off as a shell command, so a reader can tell which
	// interpreter ran the string in command.command.
	if raw["prime_python"] != true {
		t.Fatalf("raw.prime_python = %v, want true", raw["prime_python"])
	}
	if raw["prime_python_status"] != "ok" {
		t.Fatalf("raw.prime_python_status = %v, want ok", raw["prime_python_status"])
	}
	// A Python cell has no exit code. Deriving a 0/1 from `status` would put a shell's vocabulary
	// on something that is not a shell.
	if _, ok := command["exit_code"]; ok {
		t.Fatalf("command = %v, want no invented exit code", command)
	}
}

// An interrupted cell is not a failed cell, and the log has to be able to tell them apart --
// otherwise every Ctrl+C reads as an agent error.
func TestPrimeEventIpythonRecordsStatusAndErrorClass(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "call-1",
		"input":   map[string]interface{}{"code": "1/0"},
		"isError": true,
		"details": map[string]interface{}{
			"status": "error",
			"error": map[string]interface{}{
				"ename": "ZeroDivisionError", "evalue": "division by zero",
			},
		},
		"sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "tool.failed")
	raw := nested(t, event, "raw")
	if raw["prime_python_status"] != "error" {
		t.Fatalf("raw.prime_python_status = %v, want error", raw["prime_python_status"])
	}
	if raw["prime_python_error"] != "ZeroDivisionError" {
		t.Fatalf("raw.prime_python_error = %v, want ZeroDivisionError", raw["prime_python_error"])
	}
	// The code still has to reach the failed event: a cell that raised is exactly the cell an
	// investigation wants to read.
	if command := nested(t, event, "command"); command["command"] != "1/0" {
		t.Fatalf("command.command = %v, want the cell's code on the failure event", command["command"])
	}
}

// Prime Agent's kernel streams a diff for every file its helpers rewrite, so one cell that edits
// three files reports three of them. Each becomes its own file.modified, because a store that files
// one row per path is the one a "who touched this file" query can answer.
func TestPrimeEventIpythonKernelDiffsBecomeFileEvents(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "ipython", "toolCallId": "call-1",
		"input": map[string]interface{}{"code": "edit('a.py'); edit('b.py')"},
		"details": map[string]interface{}{
			"status": "ok",
			"diffs": []interface{}{
				map[string]interface{}{"path": "/repo/a.py", "oldStr": "x = 1", "newStr": "x = 2"},
				map[string]interface{}{"path": "/repo/b.py", "oldStr": "y = 1", "newStr": "y = 2"},
				// No path is not a file edit. Recording it would produce a row every file-scoped
				// query matches and none can explain.
				map[string]interface{}{"oldStr": "z = 1", "newStr": "z = 2"},
				// An edit that substituted identical text changed nothing on disk.
				map[string]interface{}{"path": "/repo/c.py", "oldStr": "same", "newStr": "same"},
			},
		},
		"sessionId": "sess-1",
	})

	var paths []string
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] != "file.modified" {
			continue
		}
		file := nested(t, event, "file")
		paths = append(paths, file["path"].(string))
		if file["diff"] == nil || !strings.Contains(file["diff"].(string), "+") {
			t.Fatalf("file.diff = %v, want a patch for %v", file["diff"], file["path"])
		}
		if file["diff_hash"] == nil || file["diff_bytes"] == nil {
			t.Fatalf("file = %v, want the diff described by a hash and a byte count", file)
		}
		// The cell's code belongs on the command.executed this event accompanies, not here: a whole
		// Python cell in command.command on a per-file row would make a command-scoped rule match
		// the cell once per file it happened to touch.
		if _, ok := event["command"]; ok {
			t.Fatalf("file.modified carries a command block: %v", event)
		}
		// The tool call id is the only thing joining this write back to the cell that made it.
		if genAI := nested(t, event, "gen_ai"); genAI["tool"] == nil {
			t.Fatalf("gen_ai = %v, want the tool call id on the file event", genAI)
		}
	}

	if len(paths) != 2 || paths[0] != "/repo/a.py" || paths[1] != "/repo/b.py" {
		t.Fatalf("file.modified paths = %v, want one per changed file", paths)
	}
}

// Prime Agent exposes no approval event at all. Its tool_call handler can block, but that is an
// extension deciding rather than an operator being asked, and recording a block as an approval
// would be indistinguishable from a decision a human actually made. This is the Pi and Cline
// posture, and it is what separates Prime Agent from Oh My Pi, which does report real ones.
func TestPrimeEventNeverSynthesizesAnApproval(t *testing.T) {
	for _, name := range supportedPrimeEventTypes() {
		if strings.Contains(name, "approval") {
			t.Fatalf("supportedPrimeEventTypes includes %q; Prime Agent publishes no approval event", name)
		}
	}

	logPath := primeTestLog(t)
	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "tool_call", "toolName": "ipython", "toolCallId": "call-1",
		"input": map[string]interface{}{"code": "import shutil; shutil.rmtree('/')"}, "sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "tool.invoked")
	if _, ok := event["approval"]; ok {
		t.Fatalf("tool.invoked carries an approval block: %v", event)
	}
}

// The `!` prefix is Prime Agent's operator shell surface, and no tool event covers it. It is the
// one command shape here the agent did not originate, so it is marked rather than merged in with
// agent-run commands.
func TestPrimeEventUserBashIsMarkedOperatorInitiated(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type": "user_bash", "command": "git push --force", "cwd": "/repo", "sessionId": "sess-1",
	})

	event := primeEventWithAction(t, logPath, "command.executed")
	if command := nested(t, event, "command"); command["command"] != "git push --force" {
		t.Fatalf("command.command = %v, want the operator's command", command["command"])
	}
	if raw := nested(t, event, "raw"); raw["prime_user_initiated"] != true {
		t.Fatalf("raw.prime_user_initiated = %v, want true", raw["prime_user_initiated"])
	}
}

// Usage and cost come off a finalized assistant message, the only place this family reports them,
// and land in gen_ai.usage rather than in a parallel per-harness field.
func TestPrimeEventMessageEndReportsUsageAndReasoning(t *testing.T) {
	logPath := primeTestLog(t)

	runHookWithInput(t, runPrimeEvent, map[string]interface{}{
		"type":      "message_end",
		"sessionId": "sess-1",
		"message": map[string]interface{}{
			"role":  "assistant",
			"model": "prime-rl-1",
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "consider the failing test"},
				map[string]interface{}{"type": "text", "text": "Fixed it."},
			},
			"usage": map[string]interface{}{
				"input": float64(120), "output": float64(30), "cacheRead": float64(8),
				"cost": map[string]interface{}{"total": 0.0042},
			},
		},
	})

	usage := nested(t, nested(t, primeEventWithAction(t, logPath, "token.usage"), "gen_ai"), "usage")
	if usage["input_tokens"] != float64(120) || usage["output_tokens"] != float64(30) {
		t.Fatalf("gen_ai.usage = %v, want the reported input and output tokens", usage)
	}
	if cacheRead, ok := usage["cache_read"].(map[string]interface{}); !ok || cacheRead["input_tokens"] != float64(8) {
		t.Fatalf("gen_ai.usage.cache_read = %v, want 8 input tokens", usage["cache_read"])
	}
	// Runtime-reported cost only; Beacon never derives it from a local pricing table.
	if usage["cost_usd"] != 0.0042 {
		t.Fatalf("gen_ai.usage.cost_usd = %v, want the runtime's reported cost", usage["cost_usd"])
	}

	reasoning := primeEventWithAction(t, logPath, "agent.reasoning")
	output := nested(t, nested(t, reasoning, "gen_ai"), "output")
	messages, _ := output["messages"].([]interface{})
	if len(messages) != 1 {
		t.Fatalf("gen_ai.output.messages = %v, want one reasoning message", output["messages"])
	}
	// The assistant's visible answer is not reasoning. Recording it as such would put the model's
	// output where a reader looking for its private deliberation expects to find it.
	first, _ := messages[0].(map[string]interface{})
	parts, _ := first["parts"].([]interface{})
	part, _ := parts[0].(map[string]interface{})
	if part["content"] != "consider the failing test" {
		t.Fatalf("reasoning part = %v, want only the thinking text", part)
	}
}
