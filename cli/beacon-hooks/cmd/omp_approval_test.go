package cmd

import "testing"

// The capability Oh My Pi has and Pi does not, and the reason it gets its own mapping rather than
// being folded into the tool events: these are decisions an operator was actually asked to make.
//
// Beacon has always refused to synthesize an approval from a tool call, because a `tool_call`
// handler that blocks is an extension deciding and recording that as an approval would be
// indistinguishable from a human's answer. Here the runtime reports the prompt and the answer, so
// the events are real and are marked `observed`.
func TestOmpApprovalRequestedIsRecorded(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_approval_requested", "sessionId": "sess-1",
		"toolName": "bash", "toolCallId": "call-1",
		"reason": "writes outside the workspace", "approvalMode": "always-ask",
	})

	event := ompEventWithAction(t, logPath, "approval.requested")
	approval := nested(t, event, "approval")
	if approval["required"] != true || approval["decision"] != "requested" {
		t.Fatalf("approval = %v, want a required, still-pending decision", approval)
	}
	// The runtime's own words for why, not a Beacon-authored sentence.
	if approval["reason"] != "writes outside the workspace" {
		t.Fatalf("approval.reason = %v, want the runtime's reason", approval["reason"])
	}
	if meta := nested(t, event, "event"); meta["category"] != "approval" {
		t.Fatalf("category = %v, want approval", meta["category"])
	}
	// A decision the operator was genuinely asked for is observed, never inferred.
	if fidelity := nested(t, event, "event")["fidelity"]; fidelity != "observed" {
		t.Fatalf("event.fidelity = %v, want observed", fidelity)
	}
}

func TestOmpApprovalResolvedRecordsTheDecision(t *testing.T) {
	for _, tc := range []struct {
		approved bool
		action   string
		decision string
	}{
		{true, "approval.allowed", "approve"},
		{false, "approval.denied", "deny"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			logPath := ompTestLog(t)

			runHookWithInput(t, runOmpEvent, map[string]interface{}{
				"type": "tool_approval_resolved", "sessionId": "sess-1",
				"toolName": "bash", "toolCallId": "call-1", "approved": tc.approved,
			})

			event := ompEventWithAction(t, logPath, tc.action)
			if approval := nested(t, event, "approval"); approval["decision"] != tc.decision {
				t.Fatalf("approval.decision = %v, want %q", approval["decision"], tc.decision)
			}
			if tool := nested(t, event, "tool"); tool["name"] != "bash" {
				t.Fatalf("tool.name = %v, want bash", tool["name"])
			}
		})
	}
}

// A resolved approval that carries no `approved` field is a denial, not an allow.
//
// The runtime states the outcome as a boolean, so a missing or non-boolean value means Beacon
// cannot see an approval -- and the safe reading of "no evidence the operator said yes" is not
// "the operator said yes". Defaulting the other way would turn a malformed payload into a clean
// record of consent.
func TestOmpApprovalWithoutAnAffirmativeIsNotAnAllow(t *testing.T) {
	for _, payload := range []map[string]interface{}{
		{"type": "tool_approval_resolved", "toolName": "bash", "toolCallId": "c1"},
		{"type": "tool_approval_resolved", "toolName": "bash", "toolCallId": "c1", "approved": "yes"},
		{"type": "tool_approval_resolved", "toolName": "bash", "toolCallId": "c1", "approved": nil},
	} {
		events := ompRuntime.endpointEvents(payload, "sess-1")
		if len(events) != 1 {
			t.Fatalf("payload %v produced %d events, want 1", payload, len(events))
		}
		if events[0].action != "approval.denied" {
			t.Fatalf("payload %v produced %q; an unreadable outcome must not be recorded as consent",
				payload, events[0].action)
		}
	}
}

// The single most load-bearing fact about any approval row: whether the prompts were on at all.
// "yolo" means the operator turned them off, which is the difference between a decision someone
// made and a decision nobody was asked to make.
func TestOmpApprovalRecordsTheApprovalMode(t *testing.T) {
	for _, mode := range []string{"always-ask", "write", "yolo"} {
		t.Run(mode, func(t *testing.T) {
			logPath := ompTestLog(t)

			runHookWithInput(t, runOmpEvent, map[string]interface{}{
				"type": "tool_approval_requested", "sessionId": "sess-1",
				"toolName": "bash", "toolCallId": "call-1", "approvalMode": mode,
			})

			event := ompEventWithAction(t, logPath, "approval.requested")
			if raw := nested(t, event, "raw"); raw["omp_approval_mode"] != mode {
				t.Fatalf("raw.omp_approval_mode = %v, want %q", raw["omp_approval_mode"], mode)
			}
		})
	}
}

// An approval carries no tool arguments, because the runtime does not put them on these events. The
// call id is the join back to the tool.invoked that does carry them -- so an investigation can go
// from "this was approved" to "this is what was approved". Without it the approval names a tool and
// nothing else.
func TestOmpApprovalJoinsToTheToolCallItDecided(t *testing.T) {
	for _, payload := range []map[string]interface{}{
		{"type": "tool_approval_requested", "toolName": "bash", "toolCallId": "call-77"},
		{"type": "tool_approval_resolved", "toolName": "bash", "toolCallId": "call-77", "approved": true},
	} {
		t.Run(payload["type"].(string), func(t *testing.T) {
			events := ompRuntime.endpointEvents(payload, "sess-1")
			if len(events) != 1 {
				t.Fatalf("produced %d events, want 1", len(events))
			}
			if got := piFamilyToolCallID(t, events[0]); got != "call-77" {
				t.Fatalf("gen_ai.tool.call.id = %q, want call-77 -- an approval that cannot be "+
					"joined to its tool call names a tool and nothing else", got)
			}
		})
	}
}

// Pi must not gain approvals by sharing the mapper. It never sends these events, so the cases are
// unreachable for it -- a stronger guarantee than a platform check, which could be forgotten.
func TestOmpOnlyEventTypesAreNotInPis(t *testing.T) {
	pi := map[string]bool{}
	for _, name := range supportedPiEventTypes() {
		pi[name] = true
	}
	for _, name := range []string{"tool_approval_requested", "tool_approval_resolved", "user_python"} {
		if pi[name] {
			t.Fatalf("Pi's extension subscribes to %q; Pi exposes no such event, and subscribing "+
				"to one would put a decision nobody made into the log", name)
		}
	}
}

// The `$` prefix runs Python in the runtime's own REPL. It is the operator's code, executed as
// literally as a shell command -- os.system("rm -rf /") is a shell command wearing a Python hat --
// so it lands in the command category where the risky-command rules can see it, marked as the
// operator's and as Python rather than passed off as a shell command.
func TestOmpUserPythonIsRecordedAsAnOperatorCommand(t *testing.T) {
	logPath := ompTestLog(t)
	code := "import os; os.system('id')"

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "user_python", "code": code, "excludeFromContext": false,
		"cwd": "/repo", "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "command.executed")
	if command := nested(t, event, "command"); command["command"] != code {
		t.Fatalf("command.command = %v, want the Python source", command["command"])
	}
	if tool := nested(t, event, "tool"); tool["name"] != "user_python" {
		t.Fatalf("tool.name = %v, want user_python -- it must be distinguishable from a shell "+
			"command that happens to have been run by the operator", tool["name"])
	}
	raw := nested(t, event, "raw")
	if raw["omp_user_initiated"] != true || raw["omp_user_python"] != true {
		t.Fatalf("raw = %v, want it marked operator-initiated and Python", raw)
	}
}

func TestOmpEmptyUserPythonProducesNothing(t *testing.T) {
	events := ompRuntime.endpointEvents(map[string]interface{}{
		"type": "user_python", "cwd": "/repo",
	}, "sess-1")
	if len(events) != 0 {
		t.Fatalf("empty user_python produced %d events, want none", len(events))
	}
}

// MCP attribution. Without it an MCP call lands in the log as a tool named
// `mcp__github_create_issue` and nothing else, so the two questions actually asked about MCP
// activity -- which server did this agent reach, and what did it call there -- have no field to
// answer them.
func TestOmpMCPToolIsAttributedToItsServer(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_result", "toolName": "mcp__github_create_issue", "toolCallId": "call-1",
		"input": map[string]interface{}{"title": "bug"}, "sessionId": "sess-1",
	})

	event := ompEventWithAction(t, logPath, "mcp.tool_invoked")
	mcp := nested(t, event, "mcp")
	if mcp["server"] != "github" || mcp["tool"] != "create_issue" {
		t.Fatalf("mcp = %v, want the github server and its create_issue tool", mcp)
	}
	if meta := nested(t, event, "event"); meta["category"] != "mcp" {
		t.Fatalf("category = %v, want mcp", meta["category"])
	}
}

// Both spellings must resolve. `mcp__<server>__<tool>` is the widely used double-underscore form;
// Oh My Pi mints `mcp__<server>_<tool>` with a single underscore. The single-underscore split
// deliberately reproduces Oh My Pi's own parseMCPToolName, ambiguity included -- a server named
// `my_server` reads as `my` in both, so Beacon's mcp.server always says what the runtime would say.
func TestPiMCPServerToolReadsBothSpellings(t *testing.T) {
	for _, tc := range []struct {
		name         string
		server, tool string
	}{
		{"mcp__github_create_issue", "github", "create_issue"},
		{"mcp__github__create_issue", "github", "create_issue"},
		{"mcp__puppeteer_screenshot", "puppeteer", "screenshot"},
		{"mcp__my_server_run", "my", "server_run"},
		// Not MCP names, and must not be forced into one.
		{"bash", "", ""},
		{"read", "", ""},
		{"my_custom_tool", "", ""},
		{"", "", ""},
		// Prefixed but with no tool half to speak of.
		{"mcp__github", "", ""},
		{"mcp__", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, tool := piMCPServerTool(tc.name)
			if server != tc.server || tool != tc.tool {
				t.Fatalf("piMCPServerTool(%q) = (%q, %q), want (%q, %q)",
					tc.name, server, tool, tc.server, tc.tool)
			}
		})
	}
}

// An approval for an MCP tool is attributed too. "Who approved a call to which server" is the
// question an approval-abuse investigation asks, and it cannot be answered from a tool name alone.
func TestOmpApprovalForAnMCPToolCarriesTheServer(t *testing.T) {
	logPath := ompTestLog(t)

	runHookWithInput(t, runOmpEvent, map[string]interface{}{
		"type": "tool_approval_resolved", "sessionId": "sess-1",
		"toolName": "mcp__github_create_issue", "toolCallId": "call-1", "approved": true,
	})

	event := ompEventWithAction(t, logPath, "approval.allowed")
	if mcp := nested(t, event, "mcp"); mcp["server"] != "github" || mcp["tool"] != "create_issue" {
		t.Fatalf("mcp = %v, want the github server and its create_issue tool", mcp)
	}
}

// A non-MCP tool must not grow an mcp block. A row with an mcp block is one every MCP-scoped query
// matches, and a bash call is not MCP activity.
func TestOmpNonMCPToolsHaveNoMCPBlock(t *testing.T) {
	for _, tool := range []string{"bash", "read", "edit", "write", "grep", "my_custom_tool"} {
		t.Run(tool, func(t *testing.T) {
			events := ompRuntime.endpointEvents(map[string]interface{}{
				"type": "tool_result", "toolName": tool,
				"input": map[string]interface{}{"command": "ls", "path": "/repo/a.go"},
			}, "sess-1")
			if len(events) != 1 {
				t.Fatalf("produced %d events, want 1", len(events))
			}
			if _, ok := events[0].fields["mcp"]; ok {
				t.Fatalf("%s grew an mcp block: %v", tool, events[0].fields["mcp"])
			}
			if events[0].action == "mcp.tool_invoked" {
				t.Fatalf("%s was recorded as MCP activity", tool)
			}
		})
	}
}
