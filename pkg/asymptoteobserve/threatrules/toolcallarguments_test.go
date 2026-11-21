package threatrules

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func toolCallEvent(action, toolName string, args interface{}) asymptoteobserve.Event {
	return asymptoteobserve.Event{
		Event:   asymptoteobserve.EventInfo{Action: action},
		Tool:    &asymptoteobserve.ToolInfo{Name: toolName},
		Session: &asymptoteobserve.SessionInfo{ID: "s1"},
		GenAI: &asymptoteobserve.GenAIInfo{Tool: &asymptoteobserve.GenAIToolInfo{
			Name: toolName,
			Call: &asymptoteobserve.GenAIToolCallInfo{ID: "call-1", Arguments: args},
		}},
	}
}

// The raw interface{} arguments stay unaddressable, as with tool-result text: only the
// derived text is exposed to CEL.
func TestToolCallArgumentsTextIsAddressableInCEL(t *testing.T) {
	if _, err := CompileMatch(`e.gen_ai.tool.call.arguments_text.matches("admin")`); err != nil {
		t.Fatalf("arguments_text does not compile: %v", err)
	}
	var found bool
	for _, f := range EventFields() {
		if f.Path == ToolCallArgumentsTextPath {
			found = true
			if f.Type != "string" {
				t.Fatalf("%s type = %q, want string", f.Path, f.Type)
			}
		}
	}
	if !found {
		t.Fatalf("%s missing from EventFields", ToolCallArgumentsTextPath)
	}
	if _, err := CompileMatch(`e.gen_ai.tool.call.arguments == "x"`); err == nil {
		t.Fatal("gen_ai.tool.call.arguments compiled; want it to stay out of the CEL schema")
	}
}

func TestToolCallArgumentsTextDerivesOnCallEvents(t *testing.T) {
	const expr = `e.gen_ai.tool.call.arguments_text.matches("(?i)\\badmin\\b") && e.gen_ai.tool.call.arguments_text.matches("(?i)\\b(UPDATE|GRANT)\\b")`
	cases := map[string]asymptoteobserve.Event{
		// Gateway: arguments arrive as the call's JSON string.
		"mcp.tool_invoked json string": toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", `{"action":"GRANT","role":"admin","user":"bob"}`),
		"tool.invoked sql string":      toolCallEvent("tool.invoked", "db_query", "UPDATE users SET role = 'admin' WHERE id = 7"),
		// Other writers report arguments parsed: an object renders as JSON, keys sorted.
		"mcp.tool_invoked object": toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", map[string]interface{}{
			"user": "bob", "role": "admin", "action": "GRANT",
		}),
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ToolCallArgumentsText(event); got == "" {
				t.Fatal("ToolCallArgumentsText empty")
			}
			if !mustMatch(t, expr, event) {
				t.Fatalf("no match; derived text = %q", ToolCallArgumentsText(event))
			}
		})
	}
	t.Run("object renders keys sorted", func(t *testing.T) {
		event := toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", map[string]interface{}{
			"user": "bob", "role": "admin", "action": "GRANT",
		})
		if got := ToolCallArgumentsText(event); got != `{"action":"GRANT","role":"admin","user":"bob"}` {
			t.Fatalf("ToolCallArgumentsText = %q", got)
		}
	})
}

func TestToolCallArgumentsTextEmptyOnResultEvents(t *testing.T) {
	// A result repeats its call's arguments, so deriving the field there too would let a
	// rule fire once on the call and once on its result for the same action.
	event := toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", `{"action":"GRANT","role":"admin","user":"bob"}`)
	event.GenAI.Tool.Call.Result = "ok"
	if got := ToolCallArgumentsText(event); got != "" {
		t.Fatalf("ToolCallArgumentsText = %q, want empty on a result event", got)
	}
	if mustMatch(t, `e.gen_ai.tool.call.arguments_text != ""`, event) {
		t.Fatal("arguments_text non-empty in CEL on a result event")
	}
	t.Run("no arguments", func(t *testing.T) {
		if got := ToolCallArgumentsText(toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", nil)); got != "" {
			t.Fatalf("ToolCallArgumentsText = %q, want empty", got)
		}
	})
	t.Run("no gen_ai", func(t *testing.T) {
		if got := ToolCallArgumentsText(asymptoteobserve.Event{Event: asymptoteobserve.EventInfo{Action: "mcp.tool_invoked"}}); got != "" {
			t.Fatalf("ToolCallArgumentsText = %q, want empty for an event without gen_ai", got)
		}
	})
}

func TestToolCallArgumentsTextEmptyWhenContentNotRetained(t *testing.T) {
	cases := map[string]*asymptoteobserve.ContentInfo{
		"not included":  {Retention: asymptoteobserve.ContentRetentionFull, Included: false},
		"metadata only": {Retention: asymptoteobserve.ContentRetentionMetadata, Included: true},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			event := toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", `{"action":"GRANT","role":"admin"}`)
			event.Content = content
			if got := ToolCallArgumentsText(event); got != "" {
				t.Fatalf("ToolCallArgumentsText = %q, want empty", got)
			}
			if mustMatch(t, `e.gen_ai.tool.call.arguments_text != ""`, event) {
				t.Fatal("arguments_text non-empty in CEL")
			}
		})
	}
}

func TestToolCallArgumentsTextIsCapped(t *testing.T) {
	long := strings.Repeat("a", ToolCallArgumentsTextLimit*3)
	event := toolCallEvent("mcp.tool_invoked", "mcp__iam_grant_role", long)
	if text := ToolCallArgumentsText(event); len(text) > ToolCallArgumentsTextLimit {
		t.Fatalf("len = %d, want <= %d", len(text), ToolCallArgumentsTextLimit)
	}
}
