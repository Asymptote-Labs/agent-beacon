package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// cursorAfterAgentResponse is Cursor's documented afterAgentResponse input: the common hook fields
// plus `text`, the assistant's reply.
func cursorAfterAgentResponse(text interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"hook_event_name": "afterAgentResponse",
		"conversation_id": "conv-reply",
		"generation_id":   "gen-9",
		"model":           "claude-opus-5-5",
		"cursor_version":  "3.1.0",
		"workspace_roots": []interface{}{"/repo"},
		"user_email":      nil,
		"transcript_path": nil,
	}
	if text != nil {
		payload["text"] = text
	}
	return payload
}

// Cursor reports the reply on afterAgentResponse and nowhere else on its hooks. The hook records it
// as agent.message, with Cursor's turn (generation_id) as prompt.id, so the reply sits in the same
// turn as the prompt and reasoning the other Cursor hooks record.
func TestCursorAgentResponseRecordsTheReply(t *testing.T) {
	reply := "The build was failing because `go.sum` was stale. I ran `go mod tidy`."
	logPath, stdout := runHookProcess(t, cursorAfterAgentResponse(reply), "--platform", "cursor", "agent-response")

	var answer map[string]interface{}
	if err := json.Unmarshal(stdout, &answer); err != nil || answer["continue"] != true {
		t.Fatalf("stdout = %q, want Cursor's {\"continue\":true} no-op", stdout)
	}
	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("wrote %d events, want only the reply: %#v", len(events), events)
	}
	message := events[0]
	if got := leaf(message, "event", "action"); got != "agent.message" {
		t.Fatalf("event.action = %q, want agent.message", got)
	}
	if got := outputText(t, message); got != reply {
		t.Fatalf("reply = %q, want %q", got, reply)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"harness", "name"}, "cursor"},
		{[]string{"harness", "collection_method"}, "hook"},
		{[]string{"session", "id"}, "conv-reply"},
		{[]string{"prompt", "id"}, "gen-9"},
		{[]string{"model"}, "claude-opus-5-5"},
	} {
		if got := leaf(message, check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}

	var texts []string
	for _, m := range chatView(t, logPath) {
		if m.Kind == asymptoteobserve.TraceMessageKindAssistant {
			texts = append(texts, m.Text)
		}
	}
	if len(texts) != 1 || texts[0] != reply {
		t.Fatalf("chat view assistant text = %q, want the reply", texts)
	}
}

// No text, no message -- and still Cursor's no-op answer, so the hook never stalls the agent.
func TestCursorAgentResponseWithoutTextWritesNothing(t *testing.T) {
	for name, value := range map[string]interface{}{"absent": nil} {
		t.Run(name, func(t *testing.T) {
			logPath, stdout := runHookProcess(t, cursorAfterAgentResponse(value), "--platform", "cursor", "agent-response")
			if !strings.Contains(string(stdout), `"continue":true`) {
				t.Fatalf("stdout = %q, want the cursor no-op", stdout)
			}
			if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
				t.Fatalf("wrote %d events with no reply: %#v", len(events), events)
			}
		})
	}
}

// `text` is generic; it is the reply only on Cursor's afterAgentResponse. Another runtime's
// payload routed here records nothing.
func TestAgentResponseReadsTextOnlyForCursor(t *testing.T) {
	payload := map[string]interface{}{"session_id": "s-1", "cwd": "/repo", "text": "a prompt, not a reply"}
	logPath, _ := runHookProcess(t, payload, "--platform", "qwen", "agent-response")
	if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
		t.Fatalf("recorded %d events under --platform qwen: %#v", len(events), events)
	}
}

// The payload is recognized as Cursor's even when the hook command carries no --platform flag, the
// same as every other Cursor hook event.
func TestAfterAgentResponseIsACursorPayload(t *testing.T) {
	if !looksLikeCursorPayload(map[string]interface{}{"hook_event_name": "afterAgentResponse"}) {
		t.Fatal("afterAgentResponse is not recognized as a Cursor hook event")
	}
}
