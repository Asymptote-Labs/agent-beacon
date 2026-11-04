package cmd

import (
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// cascadeResponsePayload is Devin Desktop's documented post_cascade_response input: the common
// Cascade fields plus tool_info.response, the Markdown of Cascade's reply since the last user input.
func cascadeResponsePayload(response interface{}) map[string]interface{} {
	info := map[string]interface{}{}
	if response != nil {
		info["response"] = response
	}
	return map[string]interface{}{
		"agent_action_name": "post_cascade_response",
		"trajectory_id":     "cascade-session",
		"execution_id":      "cascade-turn",
		"timestamp":         "2026-10-15T10:00:00Z",
		"model_name":        "SWE-2",
		"tool_info":         info,
	}
}

func TestCascadeResponseRecordsTheReply(t *testing.T) {
	reply := "### Planner Response\n\nI'll help you create that file.\n\n*Created file `/repo/a.py`*\n\n### Planner Response\n\nThe file has been created: \"a.py\""
	logPath, stdout := runHookProcess(t, cascadeResponsePayload(reply), "--platform", "devin-desktop", "cascade-response")

	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stdout = %q, want {}", stdout)
	}
	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("wrote %d events, want only the reply: %#v", len(events), events)
	}
	message := events[0]
	if got := leaf(message, "event", "action"); got != "agent.message" {
		t.Fatalf("event.action = %q, want agent.message", got)
	}
	// Verbatim, including the trailing quote and Markdown: nothing is trimmed or reparsed.
	if got := outputText(t, message); got != reply {
		t.Fatalf("reply = %q, want %q", got, reply)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"harness", "name"}, "devin-desktop"},
		{[]string{"harness", "collection_method"}, "hook"},
		{[]string{"session", "id"}, "cascade-session"},
		{[]string{"session", "execution_id"}, "cascade-turn"},
		{[]string{"raw", "cascade", "agent_action_name"}, "post_cascade_response"},
	} {
		if got := leaf(message, check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}
	// The response is stored once, as the reply, not a second time in the raw payload.
	raw, _ := message["raw"].(map[string]interface{})
	cascade, _ := raw["cascade"].(map[string]interface{})
	info, _ := cascade["tool_info"].(map[string]interface{})
	if _, dup := info["response"]; dup {
		t.Fatalf("raw.cascade.tool_info kept a second copy of the response: %#v", info)
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

// The command reads Cascade's payload only under Cascade's platform.
func TestCascadeResponseIgnoresOtherPlatforms(t *testing.T) {
	logPath, _ := runHookProcess(t, cascadeResponsePayload("not a cascade runtime"), "--platform", "devin-cli", "cascade-response")
	if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
		t.Fatalf("recorded %d events under --platform devin-cli: %#v", len(events), events)
	}
}

func TestCascadePayloadWithoutResponseLeavesTheInputAlone(t *testing.T) {
	input := cascadeResponsePayload("text")
	trimmed := cascadePayloadWithoutResponse(input)
	if _, ok := trimmed["tool_info"].(map[string]interface{})["response"]; ok {
		t.Fatal("response not removed from the copy")
	}
	if input["tool_info"].(map[string]interface{})["response"] != "text" {
		t.Fatal("removing the response from the copy changed the input")
	}
}
