package cmd

import "testing"

// piFamilyRuntimes is every runtime the shared Pi-family mapper serves. Tests that assert a
// property of the shape rather than of one product walk this, so a runtime added later inherits
// the same guarantees instead of quietly opting out of them.
var piFamilyRuntimes = []piFamily{piRuntime, ompRuntime}

func piFamilyToolCallID(t *testing.T, event normalizedEvent) string {
	t.Helper()
	genAI, ok := event.fields["gen_ai"].(map[string]interface{})
	if !ok {
		return ""
	}
	tool, ok := genAI["tool"].(map[string]interface{})
	if !ok {
		return ""
	}
	call, ok := tool["call"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := call["id"].(string)
	return id
}

// The join key. Both halves of a tool call carry the runtime's own `toolCallId`, and it is the only
// field that links the tool.invoked to whatever the completion turned into -- a file.modified, a
// command.executed, or, on Oh My Pi, the approval decision for the same execution. Before this was
// promoted, the two halves of one tool call sat in the log as unrelated rows that merely shared a
// session id and a nearby timestamp.
func TestPiFamilyPromotesTheToolCallIDOnBothHalves(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			for _, payload := range []map[string]interface{}{
				{"type": "tool_call", "toolName": "bash", "toolCallId": "call-77",
					"input": map[string]interface{}{"command": "ls"}},
				{"type": "tool_result", "toolName": "bash", "toolCallId": "call-77",
					"input": map[string]interface{}{"command": "ls"}},
			} {
				events := runtime.endpointEvents(payload, "sess-1")
				if len(events) != 1 {
					t.Fatalf("%s produced %d events, want 1", payload["type"], len(events))
				}
				if got := piFamilyToolCallID(t, events[0]); got != "call-77" {
					t.Fatalf("%s gen_ai.tool.call.id = %q, want call-77 -- without it the two "+
						"halves of one tool call cannot be joined", payload["type"], got)
				}
			}
		})
	}
}

// The call ID must survive onto whatever action the completion resolved to, not just onto the
// generic tool.completed. A file.modified with no join key cannot be tied back to the tool.invoked
// that proposed the edit, which is exactly the link an investigation follows.
func TestPiFamilyToolCallIDSurvivesOntoFileAndCommandActions(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		for _, tc := range []struct {
			tool   string
			args   map[string]interface{}
			action string
		}{
			{"edit", map[string]interface{}{"path": "/repo/main.go"}, "file.modified"},
			{"write", map[string]interface{}{"path": "/repo/new.go"}, "file.created"},
			{"read", map[string]interface{}{"path": "/repo/main.go"}, "file.read"},
			{"bash", map[string]interface{}{"command": "ls"}, "command.executed"},
			{"grep", map[string]interface{}{"pattern": "TODO"}, "tool.completed"},
		} {
			t.Run(runtime.platform+"/"+tc.tool, func(t *testing.T) {
				events := runtime.endpointEvents(map[string]interface{}{
					"type": "tool_result", "toolName": tc.tool, "toolCallId": "call-9", "input": tc.args,
				}, "sess-1")
				if len(events) != 1 || events[0].action != tc.action {
					t.Fatalf("events = %v, want a single %s", events, tc.action)
				}
				if got := piFamilyToolCallID(t, events[0]); got != "call-9" {
					t.Fatalf("%s gen_ai.tool.call.id = %q, want call-9", tc.action, got)
				}
			})
		}
	}
}

// A failed tool still gets the join key. A failure is the case an investigation is most likely to
// be reading, and it is the one where the tool.invoked that preceded it matters most.
func TestPiFamilyFailedToolKeepsTheToolCallID(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			events := runtime.endpointEvents(map[string]interface{}{
				"type": "tool_result", "toolName": "bash", "toolCallId": "call-5",
				"input": map[string]interface{}{"command": "false"}, "isError": true,
			}, "sess-1")
			if len(events) != 1 || events[0].action != "tool.failed" {
				t.Fatalf("events = %v, want a single tool.failed", events)
			}
			if got := piFamilyToolCallID(t, events[0]); got != "call-5" {
				t.Fatalf("gen_ai.tool.call.id = %q, want call-5", got)
			}
		})
	}
}

// Only an event that describes a tool action takes a join key. A session lifecycle payload or a
// prompt that happened to echo an ID is not a tool call, and giving it the join key of one would
// join it to work it did not do -- the invariant applyToolCallID exists to keep.
func TestPiFamilyNonToolEventsTakeNoToolCallID(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		for _, payload := range []map[string]interface{}{
			{"type": "session_start", "reason": "startup", "toolCallId": "call-1"},
			{"type": "session_shutdown", "reason": "quit", "toolCallId": "call-1"},
			{"type": "input", "text": "hello", "toolCallId": "call-1"},
			{"type": "message_end", "toolCallId": "call-1", "message": map[string]interface{}{
				"role":  "assistant",
				"usage": map[string]interface{}{"input": float64(1), "output": float64(1)},
			}},
		} {
			t.Run(runtime.platform+"/"+payload["type"].(string), func(t *testing.T) {
				for _, event := range runtime.endpointEvents(payload, "sess-1") {
					if got := piFamilyToolCallID(t, event); got != "" {
						t.Fatalf("%s took a tool call id (%q); it describes no tool action",
							event.action, got)
					}
				}
			})
		}
	}
}

// A payload with no call ID must still produce its event. Losing the join key is bad; losing the
// record of the tool call is worse.
func TestPiFamilyToolEventsSurviveAMissingToolCallID(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			events := runtime.endpointEvents(map[string]interface{}{
				"type": "tool_result", "toolName": "bash",
				"input": map[string]interface{}{"command": "ls"},
			}, "sess-1")
			if len(events) != 1 || events[0].action != "command.executed" {
				t.Fatalf("events = %v, want a single command.executed", events)
			}
			if got := piFamilyToolCallID(t, events[0]); got != "" {
				t.Fatalf("gen_ai.tool.call.id = %q, want it absent when the runtime sent none", got)
			}
		})
	}
}

// The user's own `!` command is not a tool call the model made, so it carries no runtime tool call
// id. It still records the command, which is the fact that matters about it.
func TestPiFamilyUserBashHasNoToolCallID(t *testing.T) {
	for _, runtime := range piFamilyRuntimes {
		t.Run(runtime.platform, func(t *testing.T) {
			events := runtime.endpointEvents(map[string]interface{}{
				"type": "user_bash", "command": "git status", "cwd": "/repo",
			}, "sess-1")
			if len(events) != 1 || events[0].action != "command.executed" {
				t.Fatalf("events = %v, want a single command.executed", events)
			}
			if got := piFamilyToolCallID(t, events[0]); got != "" {
				t.Fatalf("gen_ai.tool.call.id = %q, want none for an operator-run command", got)
			}
		})
	}
}
