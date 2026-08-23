package beaconevent

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"go.opentelemetry.io/collector/pdata/plog"
)

// firstPartContent reads the text out of a gen_ai messages value in the semconv
// shape, so the assertions below read the field the way a consumer would.
func firstPartContent(t *testing.T, messages interface{}) string {
	t.Helper()
	list, ok := messages.([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("messages = %#v, want a non-empty list", messages)
	}
	message, ok := list[0].(map[string]interface{})
	if !ok {
		t.Fatalf("message = %#v, want a map", list[0])
	}
	parts, ok := message["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		t.Fatalf("parts = %#v, want a non-empty list", message["parts"])
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("part = %#v, want a map", parts[0])
	}
	content, _ := part["content"].(string)
	return content
}

func claudeLogEvent(t *testing.T, body string, attrs map[string]interface{}) Event {
	t.Helper()
	record := plog.NewLogRecord()
	record.Body().SetStr(body)
	merged := map[string]interface{}{"service.name": "claude-code"}
	for key, value := range attrs {
		merged[key] = value
	}
	if err := record.Attributes().FromRaw(merged); err != nil {
		t.Fatalf("load attributes: %v", err)
	}
	return NewConverter(Options{}).EventFromLog(nil, record)
}

func TestClaudeAssistantResponseCarriesTheAssistantText(t *testing.T) {
	// The largest content gap on the OTLP path: Claude Code emits the model's whole
	// reply as `response` and nothing read it, so the text sat in raw.attributes and
	// the event carried no assistant output at all.
	event := claudeLogEvent(t, "claude_code.assistant_response", map[string]interface{}{
		"event.name":      "assistant_response",
		"response":        "I updated three files.",
		"response_length": 22,
		"model":           "claude-opus-5",
	})

	if event.GenAI == nil || event.GenAI.Output == nil || event.GenAI.Output.Messages == nil {
		t.Fatalf("assistant text not promoted: %#v", event.GenAI)
	}
	if got := firstPartContent(t, event.GenAI.Output.Messages); got != "I updated three files." {
		t.Fatalf("assistant text = %q, want the response attribute", got)
	}
	if event.Content == nil || !event.Content.Included || event.Content.Bytes != len("I updated three files.") {
		t.Fatalf("content marker = %#v, want the retained response described", event.Content)
	}
	// The action stays whatever the classification table says; this change is about
	// what the event carries, not what it is called.
	if event.Event.Action != "session.activity" {
		t.Fatalf("action = %q, want the classification unchanged", event.Event.Action)
	}
}

func TestClaudeFeedbackSurveyResponseIsNotAssistantText(t *testing.T) {
	// `response` means a turn only on the event named for it. feedback_survey uses
	// the same key for a survey answer, so a blanket alias would record "positive"
	// as something the model said.
	event := claudeLogEvent(t, "claude_code.feedback_survey", map[string]interface{}{
		"event.name":  "feedback_survey",
		"event_type":  "responded",
		"response":    "positive",
		"survey_type": "session",
	})

	if event.GenAI != nil && event.GenAI.Output != nil && event.GenAI.Output.Messages != nil {
		t.Fatalf("survey answer promoted as assistant text: %#v", event.GenAI.Output.Messages)
	}
	if event.Content != nil {
		t.Fatalf("survey answer produced a content marker: %#v", event.Content)
	}
}

func TestClaudeUserPromptCarriesPromptTextAndInputMessages(t *testing.T) {
	event := claudeLogEvent(t, "claude_code.user_prompt", map[string]interface{}{
		"event.name":    "user_prompt",
		"prompt":        "refactor the writer",
		"prompt_length": 19,
	})

	if event.Prompt == nil || event.Prompt.Text != "refactor the writer" {
		t.Fatalf("prompt = %#v, want the prompt attribute", event.Prompt)
	}
	if event.GenAI == nil || event.GenAI.Input == nil || event.GenAI.Input.Messages == nil {
		t.Fatalf("prompt not mirrored into gen_ai.input.messages: %#v", event.GenAI)
	}
	if got := firstPartContent(t, event.GenAI.Input.Messages); got != "refactor the writer" {
		t.Fatalf("input message text = %q, want the prompt", got)
	}
	if event.Content == nil || event.Content.Bytes != len("refactor the writer") {
		t.Fatalf("content marker = %#v, want the retained prompt described", event.Content)
	}
}

func TestSemconvNativeOutputMessagesAreNotOverwritten(t *testing.T) {
	// A source that already speaks semconv keeps its own messages; the promotions
	// exist for runtimes that report a turn under their own attribute names.
	record := plog.NewLogRecord()
	record.Body().SetStr("chat")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "generic-runtime")
	attrs.PutStr("gen_ai.completion", "promoted text")
	messages := attrs.PutEmptySlice("gen_ai.output.messages")
	native := messages.AppendEmpty().SetEmptyMap()
	native.PutStr("role", "assistant")
	nativeParts := native.PutEmptySlice("parts")
	nativePart := nativeParts.AppendEmpty().SetEmptyMap()
	nativePart.PutStr("type", "text")
	nativePart.PutStr("content", "native text")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if got := firstPartContent(t, event.GenAI.Output.Messages); got != "native text" {
		t.Fatalf("output messages = %q, want the source's own messages preserved", got)
	}
}

func TestGenericAssistantAliasIsPromoted(t *testing.T) {
	record := plog.NewLogRecord()
	record.Body().SetStr("chat")
	attrs := record.Attributes()
	attrs.PutStr("service.name", "generic-runtime")
	attrs.PutStr("gen_ai.completion", "the model replied")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.GenAI == nil || event.GenAI.Output == nil {
		t.Fatalf("generic assistant alias not promoted: %#v", event.GenAI)
	}
	if got := firstPartContent(t, event.GenAI.Output.Messages); got != "the model replied" {
		t.Fatalf("assistant text = %q, want the completion attribute", got)
	}
}

func TestClaudeMCPServerConnectionCarriesItsServer(t *testing.T) {
	// The event was already actioned mcp.connection with no mcp object on it, so
	// every rule that gates on e.mcp.server saw nothing.
	event := claudeLogEvent(t, "claude_code.mcp_server_connection", map[string]interface{}{
		"event.name":     "mcp_server_connection",
		"server_name":    "github",
		"transport_type": "stdio",
		"status":         "connected",
	})

	if event.Event.Action != "mcp.connection" {
		t.Fatalf("action = %q, want mcp.connection", event.Event.Action)
	}
	if event.MCP == nil || event.MCP.Server != "github" {
		t.Fatalf("mcp = %#v, want server github", event.MCP)
	}
}

func TestCapturedClaudeLifecycleGainsAssistantTextAndContent(t *testing.T) {
	// The captured-runtime fixture is the closest thing here to the live log the
	// content gap was measured in: before this change none of its events carried
	// assistant text or a content marker.
	_, events := capturedLogEvents(t, "claude-code-lifecycle-2.1.220.json")

	withAssistantText := 0
	withContent := 0
	for _, event := range events {
		if event.GenAI != nil && event.GenAI.Output != nil && event.GenAI.Output.Messages != nil {
			withAssistantText++
		}
		if event.Content != nil {
			withContent++
		}
	}
	if withAssistantText != 1 {
		t.Fatalf("events carrying assistant text = %d, want 1 (the assistant_response record)", withAssistantText)
	}
	if withContent != 2 {
		t.Fatalf("events carrying a content marker = %d, want 2 (the prompt and the response)", withContent)
	}
}

func TestPromotedContentIsMarkedTruncatedAtTheCollectorLimit(t *testing.T) {
	// Assistant text lands in gen_ai, which the collector sanitizes at the
	// raw-attribute limit, so that is the limit the marker has to answer against.
	long := make([]byte, asymptoteobserve.DefaultRawStringLimit+1)
	for i := range long {
		long[i] = 'x'
	}
	event := claudeLogEvent(t, "claude_code.assistant_response", map[string]interface{}{
		"event.name": "assistant_response",
		"response":   string(long),
	})
	if event.Content == nil || !event.Content.Truncated {
		t.Fatalf("content marker = %#v, want truncated at the raw limit", event.Content)
	}
	if event.Content.Bytes != len(long) {
		t.Fatalf("content bytes = %d, want the original %d", event.Content.Bytes, len(long))
	}
}
