package cmd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// All four managed extensions already forward this native message_end shape; their existing
// usage/reasoning tests contain the same thinking and text parts.
func TestPiFamilyFinalizedAssistantReplyReachesChat(t *testing.T) {
	for _, runtime := range []struct {
		platform string
		harness  string
	}{
		{"pi", "pi_cli"}, {"omp", "omp"}, {"prime", "prime_agent"}, {"omo", "omo_senpi"},
	} {
		t.Run(runtime.platform, func(t *testing.T) {
			input := map[string]interface{}{
				"type": "message_end", "sessionId": "final-reply-session",
				"message": map[string]interface{}{
					"role": "assistant", "model": "test-model",
					"content": []interface{}{
						map[string]interface{}{"type": "thinking", "thinking": "compare the approaches"},
						map[string]interface{}{"type": "text", "text": "First paragraph."},
						map[string]interface{}{"type": "text", "text": "Second paragraph."},
					},
					"usage": map[string]interface{}{
						"input": 10, "output": 5, "cost": map[string]interface{}{"total": 0.0042},
					},
				},
			}
			started := time.Now()
			logPath, stdout := runHookProcess(t, input, "--platform", runtime.platform, runtime.platform+"-event")
			finished := time.Now()
			var recorded struct {
				Recorded int `json:"recorded"`
			}
			if err := json.Unmarshal(stdout, &recorded); err != nil {
				t.Fatal(err)
			}
			events := endpointEvents(t, logPath)
			messages := assistantMessages(t, logPath)
			if len(messages) != 1 {
				t.Fatalf("finalized assistant replies = %d, want 1; actions=%v recorded=%d", len(messages), piEventActions(t, logPath), recorded.Recorded)
			}
			wantText := "First paragraph.\nSecond paragraph."
			if got := outputText(t, messages[0]); got != wantText {
				t.Fatalf("assistant text = %q, want %q", got, wantText)
			}
			if recorded.Recorded != 3 || len(events) != 3 {
				t.Fatalf("recorded=%d events=%d, want reply, reasoning and usage once each", recorded.Recorded, len(events))
			}
			ids := map[string]bool{}
			usageEvents := 0
			for _, event := range events {
				for _, check := range []struct {
					path []string
					want string
				}{
					{[]string{"harness", "name"}, runtime.harness},
					{[]string{"harness", "collection_method"}, asymptoteobserve.CollectionMethodPlugin},
					{[]string{"session", "id"}, "final-reply-session"},
					{[]string{"model"}, "test-model"},
					{[]string{"event", "fidelity"}, asymptoteobserve.FidelityObserved},
				} {
					if got := leaf(event, check.path...); got != check.want {
						t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
					}
				}
				id := leaf(event, "event", "id")
				if id == "" || ids[id] {
					t.Errorf("missing or duplicate event ID %q", id)
				}
				ids[id] = true
				if got := leaf(event, "trace", "id"); got != asymptoteobserve.DerivedTraceIDForSession(runtime.harness, "final-reply-session") {
					t.Errorf("trace.id = %q", got)
				}
				timestamp, err := time.Parse(time.RFC3339Nano, leaf(event, "timestamp"))
				if err != nil || timestamp.Before(started) || timestamp.After(finished) {
					t.Errorf("timestamp=%v err=%v, want actual hook emission time within [%v, %v]", timestamp, err, started, finished)
				}
				genAI := nested(t, event, "gen_ai")
				if leaf(event, "event", "action") == "token.usage" {
					usageEvents++
					usage := nested(t, event, "gen_ai", "usage")
					if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) || usage["cost_usd"] != 0.0042 {
						t.Errorf("usage = %#v, want the single runtime-reported token/cost record", usage)
					}
					if _, present := genAI["output"]; present {
						t.Error("reply/reasoning output leaked into usage event")
					}
				} else if _, present := genAI["usage"]; present {
					t.Error("usage duplicated onto a content event")
				}
			}
			if usageEvents != 1 {
				t.Fatalf("usage events = %d, want 1", usageEvents)
			}
			reasoning := piEventWithAction(t, logPath, "agent.reasoning")
			output := nested(t, reasoning, "gen_ai", "output")
			if got := asymptoteobserve.GenAIText(output["messages"], asymptoteobserve.RoleAssistant, asymptoteobserve.GenAIPartTypeReasoning); !reflect.DeepEqual(got, []string{"compare the approaches"}) {
				t.Fatalf("reasoning = %q, want only thinking", got)
			}
			if outputText(t, reasoning) != "" {
				t.Error("visible answer was recorded as reasoning")
			}
			var replies []string
			for _, message := range chatView(t, logPath) {
				if message.Kind == asymptoteobserve.TraceMessageKindAssistant {
					replies = append(replies, message.Text)
				}
			}
			if !reflect.DeepEqual(replies, []string{wantText}) {
				t.Fatalf("chat replies = %q, want the finalized visible answer once", replies)
			}
		})
	}
}

func TestPiFinalizedAssistantReplyOnlyRecordsVisibleText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		role    string
		content []interface{}
		want    string
	}{
		{"text-only", "assistant", []interface{}{map[string]interface{}{"type": "text", "text": "The final answer."}}, "The final answer."},
		{"blank-text", "assistant", []interface{}{map[string]interface{}{"type": "text", "text": " \n\t"}}, ""},
		{"no-text", "assistant", nil, ""},
		{"non-text-parts", "assistant", []interface{}{map[string]interface{}{"type": "toolCall", "name": "read"}}, ""},
		{"user", "user", []interface{}{map[string]interface{}{"type": "text", "text": "User text"}}, ""},
		{"tool-result", "toolResult", []interface{}{map[string]interface{}{"type": "text", "text": "Tool text"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, stdout := runHookProcess(t, map[string]interface{}{
				"type": "message_end", "sessionId": "reply-only-session",
				"message": map[string]interface{}{"role": tc.role, "content": tc.content},
			}, "--platform", "pi", "pi-event")
			events := endpointEventsIfAny(t, logPath)
			if tc.want == "" {
				if len(events) != 0 || strings.TrimSpace(string(stdout)) != `{"recorded":0}` {
					t.Fatalf("events=%v stdout=%s, want no telemetry for absent assistant text", events, stdout)
				}
				return
			}
			if len(events) != 1 || leaf(events[0], "event", "action") != "agent.message" || outputText(t, events[0]) != tc.want {
				t.Fatalf("events=%#v, want just the visible reply %q", events, tc.want)
			}
		})
	}
}

func TestPiFinalizedAssistantReplyIsRedactedAndBounded(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	reply := "Use key " + secret + " then " + strings.Repeat("x", 2*asymptoteobserve.DefaultStringLimit)
	logPath, _ := runHookProcess(t, map[string]interface{}{
		"type": "message_end", "sessionId": "bounded-reply",
		"message": map[string]interface{}{
			"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": reply}},
		},
	}, "--platform", "pi", "pi-event")
	messages := assistantMessages(t, logPath)
	if len(messages) != 1 {
		t.Fatalf("assistant replies = %d, want 1", len(messages))
	}
	stored := outputText(t, messages[0])
	if strings.Contains(stored, secret) || len(stored) >= len(reply) {
		t.Fatalf("stored reply retained secret or unbounded content: length=%d", len(stored))
	}
	content := nested(t, messages[0], "content")
	if content["truncated"] != true || content["redacted"] != true || content["bytes"] != float64(len(reply)) || content["hash"] == "" {
		t.Fatalf("content marker = %#v, want original size/hash and sanitization flags", content)
	}
	encoded, err := json.Marshal(messages[0])
	if err != nil || len(encoded) > 64*1024 {
		t.Fatalf("encoded event bytes=%d err=%v, want valid bounded JSON", len(encoded), err)
	}
}

func TestPiFinalizedAssistantReplyPreservesPreparedIdentifiers(t *testing.T) {
	fields := map[string]interface{}{
		"session": map[string]interface{}{"id": "session-1"},
		"prompt":  map[string]interface{}{"id": "prompt-1"},
		"trace":   map[string]interface{}{"id": "trace-1", "span_id": "span-1"},
	}
	before := cloneFields(fields)
	events := piRuntime.messageEndEvents(map[string]interface{}{
		"message": map[string]interface{}{
			"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Reply"}},
		},
	}, fields)
	if len(events) != 1 || events[0].action != "agent.message" {
		t.Fatalf("events=%v, want the finalized reply", events)
	}
	for _, key := range []string{"session", "prompt", "trace"} {
		if !reflect.DeepEqual(events[0].fields[key], before[key]) {
			t.Errorf("%s = %v, want prepared identifiers %v", key, events[0].fields[key], before[key])
		}
	}
	if !reflect.DeepEqual(fields, before) {
		t.Fatalf("reply mutated shared base fields: %v", fields)
	}
}
