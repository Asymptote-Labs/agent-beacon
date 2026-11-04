package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// omoTestLog puts a Senpi extension run in a temp endpoint log and returns its path.
func omoTestLog(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "omo"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	return logPath
}

func omoEventActions(t *testing.T, logPath string) []string {
	t.Helper()
	var actions []string
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		actions = append(actions, meta["action"].(string))
	}
	return actions
}

func omoEventWithAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			return event
		}
	}
	t.Fatalf("no %s event in log; got %v", action, omoEventActions(t, logPath))
	return nil
}

// omoPayloads returns one payload per supported event type, each carrying the minimum its branch
// needs to emit. The tool fixtures use `bash`, Senpi's ordinary default tool -- unlike Prime Agent,
// Senpi kept pi-mono's built-in tool set (bash, read, edit, write, grep, find, ls) rather than
// routing everything through a single kernel tool.
func omoPayloads() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"session_start":    {"type": "session_start", "reason": "startup"},
		"session_shutdown": {"type": "session_shutdown", "reason": "quit"},
		"input":            {"type": "input", "text": "do the thing", "source": "interactive"},
		"tool_call": {"type": "tool_call", "toolName": "bash", "toolCallId": "c1",
			"input": map[string]interface{}{"command": "ls"}},
		"tool_result": {"type": "tool_result", "toolName": "bash", "toolCallId": "c1",
			"input": map[string]interface{}{"command": "ls"}},
		"user_bash": {"type": "user_bash", "command": "git status", "cwd": "/repo"},
		"message_end": {"type": "message_end", "message": map[string]interface{}{
			"role":  "assistant",
			"usage": map[string]interface{}{"input": float64(10), "output": float64(5)},
		}},
	}
}

// An unrecognized type is silent rather than generic. Senpi publishes more than thirty event types
// -- provider request/response internals, streaming message and tool-execution updates, turn
// bookkeeping, compaction, model-select and TUI plumbing -- and a future one becoming an
// undifferentiated row would fill the log with records no investigation asks for.
func TestOmoEventUnknownTypeProducesNothing(t *testing.T) {
	for _, name := range []string{
		"message_update", "message_start", "before_provider_request", "after_provider_response",
		"tool_execution_start", "tool_execution_update", "tool_execution_end", "turn_start", "turn_end",
		"agent_start", "agent_end", "agent_settled", "model_select", "resources_discover",
		"thinking_level_select", "session_compact", "session_abort", "input_disposition",
		"", "totally_new",
	} {
		events := omoRuntime.endpointEvents(map[string]interface{}{"type": name}, "sess-1")
		if len(events) != 0 {
			t.Fatalf("Senpi event type %q produced %d events, want none", name, len(events))
		}
	}
}

// Senpi, Pi, Oh My Pi and Prime Agent are separately installed products that one machine can run
// side by side. Sharing the mapper must not mean sharing identity: every event has to name the
// runtime that produced it, in the harness name, in the message, and in the `raw` block an operator
// reads.
func TestOmoEventIsAttributedToOmoSenpiNotPi(t *testing.T) {
	logPath := omoTestLog(t)

	runHookWithInput(t, runOmoEvent, map[string]interface{}{
		"type": "session_start", "reason": "startup", "sessionId": "sess-1",
	})

	event := omoEventWithAction(t, logPath, "session.started")

	// The `--platform` is `omo`; the harness name events are written under is `omo_senpi`.
	// Normalizing at write time is what keeps one Senpi session from being split across two
	// spellings in any query that groups by harness.name -- and from being merged into Pi's, Oh My
	// Pi's or Prime Agent's.
	harness := nested(t, event, "harness")
	if harness["name"] != "omo_senpi" {
		t.Fatalf("harness.name = %v, want omo_senpi", harness["name"])
	}

	if event["message"] != "Senpi session started" {
		t.Fatalf("message = %v, want it to name Senpi", event["message"])
	}

	raw := nested(t, event, "raw")
	if _, ok := raw["omo"]; !ok {
		t.Fatalf("raw = %v, want the verbatim payload under an omo key", raw)
	}
	for _, other := range []string{"pi", "omp", "prime"} {
		if _, ok := raw[other]; ok {
			t.Fatalf("raw = %v, want no %s key on a Senpi event", raw, other)
		}
	}
	if raw["omo_session_reason"] != "startup" {
		t.Fatalf("raw.omo_session_reason = %v, want startup", raw["omo_session_reason"])
	}
}

// One shared shape, four identities. The shared core in pi_family.go must never start hardcoding
// one runtime's name -- which is the way a refactor like that goes wrong silently.
func TestPiAndOmoProduceTheSameShapeUnderDifferentIdentities(t *testing.T) {
	shared := map[string]bool{}
	for _, name := range supportedPiEventTypes() {
		shared[name] = true
	}
	for name, payload := range omoPayloads() {
		if !shared[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			piEvents := piRuntime.endpointEvents(cloneFields(payload), "sess-1")
			omoEvents := omoRuntime.endpointEvents(cloneFields(payload), "sess-1")

			if len(piEvents) != len(omoEvents) {
				t.Fatalf("pi produced %d events and omo %d for %q; the shared mapper should emit "+
					"the same events for the same payload", len(piEvents), len(omoEvents), name)
			}
			for i := range piEvents {
				if piEvents[i].action != omoEvents[i].action {
					t.Fatalf("action[%d] = %q (pi) vs %q (omo)", i, piEvents[i].action, omoEvents[i].action)
				}
				if piEvents[i].message == omoEvents[i].message {
					t.Fatalf("event[%d] message %q is identical for both runtimes; a reader could "+
						"not tell which one produced it", i, piEvents[i].message)
				}
				if _, ok := omoEvents[i].fields["raw"].(map[string]interface{})["omo"]; !ok {
					t.Fatalf("omo event[%d] raw block is not keyed by omo: %v", i, omoEvents[i].fields["raw"])
				}
			}
		})
	}
}

// Senpi exposes no approval event at all. Its tool_call handler can block, but that is an extension
// deciding rather than an operator being asked, and its permission-system builtin owns the real
// approval prompt without publishing it as an extension event. This is the Pi and Prime Agent
// posture, and it is what separates Senpi from Oh My Pi, which does report real ones.
func TestOmoEventNeverSynthesizesAnApproval(t *testing.T) {
	for _, name := range supportedOmoEventTypes() {
		if strings.Contains(name, "approval") {
			t.Fatalf("supportedOmoEventTypes includes %q; Senpi publishes no approval event", name)
		}
	}

	logPath := omoTestLog(t)
	runHookWithInput(t, runOmoEvent, map[string]interface{}{
		"type": "tool_call", "toolName": "bash", "toolCallId": "call-1",
		"input": map[string]interface{}{"command": "rm -rf /tmp/x"}, "sessionId": "sess-1",
	})

	event := omoEventWithAction(t, logPath, "tool.invoked")
	if _, ok := event["approval"]; ok {
		t.Fatalf("tool.invoked carries an approval block: %v", event)
	}
}

// The `!` prefix is Senpi's operator shell surface, and no tool event covers it. It is the one
// command shape here the agent did not originate, so it is marked rather than merged in with
// agent-run commands.
func TestOmoEventUserBashIsMarkedOperatorInitiated(t *testing.T) {
	logPath := omoTestLog(t)

	runHookWithInput(t, runOmoEvent, map[string]interface{}{
		"type": "user_bash", "command": "git push --force", "cwd": "/repo", "sessionId": "sess-1",
	})

	event := omoEventWithAction(t, logPath, "command.executed")
	if command := nested(t, event, "command"); command["command"] != "git push --force" {
		t.Fatalf("command.command = %v, want the operator's command", command["command"])
	}
	if raw := nested(t, event, "raw"); raw["omo_user_initiated"] != true {
		t.Fatalf("raw.omo_user_initiated = %v, want true", raw["omo_user_initiated"])
	}
}
