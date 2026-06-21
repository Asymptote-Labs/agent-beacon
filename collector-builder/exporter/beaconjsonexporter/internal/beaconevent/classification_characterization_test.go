package beaconevent

import "testing"

// These are characterization tests: they pin the CURRENT observable behavior of the
// converter's event-classification functions so the upcoming table-driven rewrites of
// NormalizeHarnessName / InferAction (and helpers) can be proven behavior-preserving.
// If a rewrite changes any mapping here, that is a deliberate decision that must update
// the expectation with justification — not an incidental drift.

func attrsOf(pairs ...string) map[string]interface{} {
	m := make(map[string]interface{}, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

func TestNormalizeHarnessNameCharacterization(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"cowork", "claude_cowork"},
		{"co-work", "claude_cowork"},
		{"claude_agent_sdk", "claude_agent_sdk"},
		{"claude-agent-sdk", "claude_agent_sdk"},
		{"claude agent sdk", "claude_agent_sdk"},
		{"claude_code", "claude_code"},
		{"claude-code", "claude_code"},
		{"claude code", "claude_code"},
		{"claude_code.session", "claude_code"},
		{"claude", "claude_code"},
		{"my claude thing", "claude_code"},
		{"openclaw", "openclaw_gateway"},
		{"open-claw", "openclaw_gateway"},
		{"antigravity", "antigravity_cli"},
		{"anti-gravity", "antigravity_cli"},
		{"codex", "codex_cli"},
		{"gemini", "gemini_cli"},
		{"copilot-chat", "vscode_copilot"},
		{"github-copilot", "copilot_cli"},
		{"copilot_cli", "copilot_cli"},
		{"copilot", "copilot_cli"},
		// Unrecognized but non-empty: returned verbatim (original case preserved).
		{"WeirdHarness", "WeirdHarness"},
	}
	for _, c := range cases {
		if got := NormalizeHarnessName(c.in); got != c.want {
			t.Errorf("NormalizeHarnessName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHarnessNameCharacterization(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]interface{}
		hints []string
		want  string
	}{
		{"explicit beacon.harness.name wins", attrsOf("beacon.harness.name", "codex"), nil, "codex_cli"},
		{"explicit harness.name", attrsOf("harness.name", "gemini"), nil, "gemini_cli"},
		{"service.name fallback", attrsOf("service.name", "codex"), nil, "codex_cli"},
		{"hint used when no attrs", nil, []string{"gemini"}, "gemini_cli"},
		{"unrecognized non-empty returned verbatim", attrsOf("service.name", "weirdname"), nil, "weirdname"},
		{"empty everything defaults to otel", nil, nil, "otel"},
	}
	for _, c := range cases {
		if got := HarnessName(c.attrs, c.hints...); got != c.want {
			t.Errorf("%s: HarnessName(%v, %v) = %q, want %q", c.name, c.attrs, c.hints, got, c.want)
		}
	}
}

func TestInferActionCharacterization(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]interface{}
		fallback string
		want     string
	}{
		{"copilot harness delegates (execute_hook)", attrsOf("beacon.harness.name", "copilot", "gen_ai.operation.name", "execute_hook"), "", ActionApprovalRequested},
		{"mcp tools/call", attrsOf("mcp.method.name", "tools/call"), "", ActionMCPToolInvoked},
		{"mcp resources/read of file uri", attrsOf("mcp.method.name", "resources/read", "mcp.resource.uri", "file:///tmp/x"), "", ActionFileRead},
		{"operation execute_tool", attrsOf("gen_ai.operation.name", "execute_tool"), "", ActionToolInvoked},
		{"chat with prompt content", attrsOf("gen_ai.operation.name", "chat", "gen_ai.prompt", "hello"), "", ActionPromptSubmitted},
		{"has tool call", attrsOf("gen_ai.tool.call.id", "call_123"), "", ActionToolInvoked},
		{"gemini user_prompt", attrsOf("event.name", "gemini_cli.user_prompt"), "", ActionPromptSubmitted},
		{"gemini tool_call mcp", attrsOf("event.name", "gemini_cli.tool_call", "tool_type", "mcp"), "", ActionMCPToolInvoked},
		{"gemini file_operation read", attrsOf("event.name", "gemini_cli.file_operation", "operation", "read"), "", ActionFileRead},
		{"approval_mode_switch", attrsOf("event.name", "approval_mode_switch"), "", ActionApprovalRequested},
		{"text contains user_input", attrsOf("event.name", "user_input"), "", ActionPromptSubmitted},
		{"text contains mcp", attrsOf("event.name", "mcp_activity"), "", ActionMCPToolInvoked},
		{"text contains shell", attrsOf("event.name", "shell"), "", ActionCommandExecuted},
		{"text contains write file", attrsOf("event.name", "write_file"), "", ActionFileModified},
		{"text contains approval", attrsOf("event.name", "approval"), "", ActionApprovalRequested},
		{"default tool.invoked", attrsOf("event.name", "randomthing"), "", ActionToolInvoked},
	}
	for _, c := range cases {
		if got := InferAction(c.attrs, c.fallback); got != c.want {
			t.Errorf("%s: InferAction(%v, %q) = %q, want %q", c.name, c.attrs, c.fallback, got, c.want)
		}
	}
}

func TestCopilotActionCharacterization(t *testing.T) {
	cases := []struct {
		name      string
		attrs     map[string]interface{}
		operation string
		text      string
		want      string
	}{
		{"session start event", attrsOf("event.name", "copilot_chat.session.start"), "", "", ActionSessionActivity},
		{"cloud session invoke", attrsOf("event.name", "copilot_chat.cloud.session.invoke"), "", "", ActionSessionActivity},
		{"tool call success", attrsOf("event.name", "copilot_chat.tool.call"), "", "", ActionToolInvoked},
		{"tool call failed via success=false", attrsOf("event.name", "copilot_chat.tool.call", "success", "false"), "", "", ActionToolFailed},
		{"tool call failed via error.type", attrsOf("event.name", "copilot_chat.tool.call", "error.type", "boom"), "", "", ActionToolFailed},
		{"edit feedback", attrsOf("event.name", "copilot_chat.edit.feedback"), "", "", ActionFileModified},
		{"inline done", attrsOf("event.name", "copilot_chat.inline.done"), "", "", ActionFileModified},
		{"invoke_agent with user request", attrsOf("copilot_chat.user_request", "do it"), "invoke_agent", "", ActionPromptSubmitted},
		{"invoke_agent without user request", nil, "invoke_agent", "", ActionSessionActivity},
		{"execute_hook", nil, "execute_hook", "", ActionApprovalRequested},
		{"chat from agent initiator", attrsOf("github.copilot.initiator", "agent"), "chat", "", ActionSessionActivity},
		{"chat with nonzero turn id", attrsOf("github.copilot.turn_id", "3"), "chat", "", ActionSessionActivity},
		{"chat default user prompt", nil, "chat", "", ActionPromptSubmitted},
		{"execute_tool", nil, "execute_tool", "", ActionToolInvoked},
		{"permission in text", nil, "", "needs permission", ActionApprovalRequested},
		{"default", nil, "", "", ActionToolInvoked},
	}
	for _, c := range cases {
		if got := CopilotAction(c.attrs, c.operation, c.text); got != c.want {
			t.Errorf("%s: CopilotAction(%v, %q, %q) = %q, want %q", c.name, c.attrs, c.operation, c.text, got, c.want)
		}
	}
}

func TestGeminiActionCharacterization(t *testing.T) {
	toolCases := []struct {
		name  string
		attrs map[string]interface{}
		want  string
	}{
		{"tool_type mcp", attrsOf("tool_type", "mcp"), ActionMCPToolInvoked},
		{"mcp_server_name set", attrsOf("mcp_server_name", "srv"), ActionMCPToolInvoked},
		{"plain tool", nil, ActionToolInvoked},
	}
	for _, c := range toolCases {
		if got := GeminiToolAction(c.attrs); got != c.want {
			t.Errorf("%s: GeminiToolAction(%v) = %q, want %q", c.name, c.attrs, got, c.want)
		}
	}

	fileCases := []struct {
		name  string
		attrs map[string]interface{}
		want  string
	}{
		{"read", attrsOf("operation", "read"), ActionFileRead},
		{"create", attrsOf("operation", "create"), ActionFileCreated},
		{"other defaults to modified", attrsOf("operation", "rename"), ActionFileModified},
		{"missing defaults to modified", nil, ActionFileModified},
	}
	for _, c := range fileCases {
		if got := GeminiFileAction(c.attrs); got != c.want {
			t.Errorf("%s: GeminiFileAction(%v) = %q, want %q", c.name, c.attrs, got, c.want)
		}
	}
}

func TestEventCategoryCharacterization(t *testing.T) {
	cases := []struct {
		action, explicit, want string
	}{
		{"tool.invoked", "override", "override"}, // explicit wins
		{"prompt.submitted", "", "prompt"},
		{"command.executed", "", "command"},
		{"file.read", "", "file"},
		{"mcp.tool_invoked", "", "mcp"},
		{"approval.requested", "", "approval"},
		{"policy.updated", "", "approval"},
		{"session.started", "", "session"},
		{"metric.recorded", "", "metric"},
		{"tool.invoked", "", "tool"},
		{"unknown.action", "", ""},
	}
	for _, c := range cases {
		if got := EventCategory(c.action, c.explicit); got != c.want {
			t.Errorf("EventCategory(%q, %q) = %q, want %q", c.action, c.explicit, got, c.want)
		}
	}
}
