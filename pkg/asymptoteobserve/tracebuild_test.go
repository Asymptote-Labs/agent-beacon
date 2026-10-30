package asymptoteobserve

import (
	"encoding/json"
	"reflect"
	"testing"
)

func traceTestEvents() []Event {
	start := shareEventAt("2026-10-01T10:00:00Z", "claude", CollectionMethodHook, "session.started", "session")
	start.Event.ID = "t-start"
	start.Message = "Claude Code session started"
	start.Trace = &TraceInfo{ID: "abc", SpanID: "root"}
	prompt := shareEventAt("2026-10-01T10:00:01Z", "claude", CollectionMethodHook, "prompt.submitted", "prompt")
	prompt.Prompt = &PromptInfo{ID: " p-1 ", Text: "explain the cache"}
	prompt.Content = &ContentInfo{Retention: "full", Included: true}
	prompt.Trace = &TraceInfo{ID: "abc", SpanID: "child", ParentSpanID: "root"}
	reply := shareEventAt("2026-10-01T10:00:02Z", "opencode", CollectionMethodPlugin, "agent.response", "session")
	reply.Event.ID = "t-reply"
	reply.Message = "opencode assistant response captured"
	reply.Model = "claude-sonnet-5-5"
	reply.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("It memoizes lookups.")},
		Usage: &GenAIUsageInfo{InputTokens: int64Ptr(10), OutputTokens: int64Ptr(4)}}
	reply.Content = &ContentInfo{Retention: "full", Included: true, Redacted: true}
	reply.Branch = "main"
	reply.Raw = map[string]interface{}{"namespace_slug": "acme", "remote_event_count": float64(9)}
	return []Event{start, prompt, reply}
}

func TestTraceEventFromEvent(t *testing.T) {
	events := traceTestEvents()
	te := TraceEventFromEvent(events[1], "line-7", 3)
	if te.ID != "line-7" || te.SourceEventID != "line-7" || te.Number != 3 {
		t.Fatalf("identity = %+v", te)
	}
	if te.Type != "user_message" || te.Actor != "user" || te.PromptID != "p-1" || te.Content == nil || te.Content.Text != "explain the cache" {
		t.Fatalf("prompt = %+v", te)
	}
	if te.Trace == nil || te.Trace.SpanID != "child" || te.Trace.ParentSpanID != "root" {
		t.Fatalf("trace = %+v", te.Trace)
	}
	// OpenCode records the assistant's text as agent.response.
	reply := TraceEventFromEvent(events[2], "", 1)
	if reply.ID != "t-reply" || reply.Type != "agent_message" || reply.Actor != "assistant" || reply.Content.Text != "It memoizes lookups." || reply.Summary != "opencode assistant response captured" {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Usage == nil || reply.Usage.InputTokens != 10 || reply.Model != "claude-sonnet-5-5" {
		t.Fatalf("usage = %+v, model = %q", reply.Usage, reply.Model)
	}
}

func TestAssistantActionsRetainTheirAnswerInTraceMessages(t *testing.T) {
	for _, tc := range []struct{ action, category string }{
		{"agent.message", "agent"},
		{"assistant.message", "agent"},
		{"agent.response", "agent"},
		{"agent.response.completed", "agent"},
		{"agent.plan", "agent"},
		{"agent.response.completed", "session"},
	} {
		t.Run(tc.action+"/"+tc.category, func(t *testing.T) {
			event := shareEventAt("2026-10-01T10:00:02Z", "chatgpt_web", CollectionMethodOTLP, tc.action, tc.category)
			event.Event.ID = "answer-event"
			event.Message = "chatgpt_web assistant response completed"
			event.Model = "gpt-5"
			event.GenAI = &GenAIInfo{
				Output: &GenAIOutputInfo{Messages: []interface{}{
					map[string]interface{}{"role": RoleAssistant, "parts": []interface{}{
						map[string]interface{}{"type": GenAIPartTypeText, "content": "The retained answer."},
						map[string]interface{}{"type": GenAIPartTypeReasoning, "content": "Private reasoning."},
					}},
				}},
				Usage: &GenAIUsageInfo{InputTokens: int64Ptr(10), OutputTokens: int64Ptr(4)},
			}
			event.Content = RetainedContent("The retained answer.", DefaultStringLimit)
			builder := NewTraceBuilder("browser-answer", event)
			projected := builder.Add(event, TraceEventFromEvent(event, "", 1))
			if projected.Type != "agent_message" || projected.Actor != "assistant" || projected.Content == nil || projected.Content.Text != "The retained answer." {
				t.Fatalf("assistant projection = %+v", projected)
			}
			if projected.ID != event.Event.ID || projected.Action != tc.action || projected.Summary != event.Message || projected.Model != event.Model ||
				projected.Usage == nil || projected.Usage.InputTokens != 10 || projected.Usage.OutputTokens != 4 {
				t.Fatalf("assistant metadata changed = %+v", projected)
			}
			bundle := builder.Bundle(TraceProvenanceV1{})
			messages := TraceMessages(bundle)
			if len(messages) != 1 || messages[0].Role != TraceRoleAssistant || messages[0].Kind != TraceMessageKindAssistant || messages[0].Text != "The retained answer." {
				t.Fatalf("assistant messages = %+v", messages)
			}
			if messages[0].ID != event.Event.ID || messages[0].Model != event.Model || messages[0].TokenUsage == nil || messages[0].TokenUsage.OutputTokens != 4 {
				t.Fatalf("message metadata changed = %+v", messages[0])
			}
			if bundle.Summary.LocalMessageCount != 1 {
				t.Fatalf("message count = %d, want 1", bundle.Summary.LocalMessageCount)
			}
		})
	}
}

func TestTextlessAssistantCompletionDoesNotInventAnAnswer(t *testing.T) {
	for _, category := range []string{"agent", "session"} {
		t.Run(category, func(t *testing.T) {
			event := shareEventAt("2026-10-01T10:00:02Z", "opencode", CollectionMethodPlugin, "agent.response.completed", category)
			event.Event.ID = "textless-completion"
			event.Message = "opencode assistant response completed"
			projected := TraceEventFromEvent(event, "", 1)
			messages := TraceMessages(TraceBundleV1{Events: []TraceEventV1{projected}})
			if len(messages) != 0 {
				t.Fatalf("textless completion invented assistant text: %+v", messages)
			}
		})
	}
}

func TestOpenCodeCompletionKeepsOneAssistantAnswer(t *testing.T) {
	prompt := shareEventAt("2026-10-01T10:00:00Z", "opencode", CollectionMethodPlugin, "prompt.submitted", "prompt")
	prompt.Event.ID = "opencode-prompt"
	prompt.Prompt = &PromptInfo{Text: "Explain the lookup cache."}
	response := shareEventAt("2026-10-01T10:00:01Z", "opencode", CollectionMethodPlugin, "agent.response", "session")
	response.Event.ID = "opencode-answer"
	response.Message = "opencode assistant response captured"
	response.GenAI = &GenAIInfo{Output: &GenAIOutputInfo{Messages: TextOutputMessages("It memoizes lookups.")}}
	completion := shareEventAt("2026-10-01T10:00:02Z", "opencode", CollectionMethodPlugin, "agent.response.completed", "session")
	completion.Event.ID = "opencode-completion"
	completion.Message = "opencode assistant response completed"
	completion.GenAI = &GenAIInfo{Response: &GenAIResponseInfo{ID: "assistant-turn"}, Usage: &GenAIUsageInfo{
		InputTokens: int64Ptr(10), OutputTokens: int64Ptr(4),
	}}
	builder := NewTraceBuilder("opencode-turn", prompt)
	for i, event := range []Event{prompt, response, completion} {
		builder.Add(event, TraceEventFromEvent(event, "", i+1))
	}
	bundle := builder.Bundle(TraceProvenanceV1{})
	if bundle.Summary.LocalMessageCount != 2 || bundle.Summary.EventCount != 3 || bundle.Events[2].Type != "session" {
		t.Fatalf("completion changed message count or lifecycle type: summary=%+v events=%+v", bundle.Summary, bundle.Events)
	}
	if bundle.Summary.Title != prompt.Prompt.Text || bundle.Summary.Preview != prompt.Prompt.Text ||
		bundle.Summary.TokenUsage == nil || bundle.Summary.TokenUsage.InputTokens != 10 || bundle.Summary.TokenUsage.OutputTokens != 4 {
		t.Fatalf("completion changed title, preview or usage: %+v", bundle.Summary)
	}
	messages := TraceMessages(bundle)
	if len(messages) != 2 || messages[0].Role != TraceRoleUser || messages[1].Role != TraceRoleAssistant || messages[1].Text != "It memoizes lookups." {
		t.Fatalf("completion changed the conversation: %+v", messages)
	}
}

func TestMetadataOnlyBrowserCompletionStillCountsAsAnAnswer(t *testing.T) {
	event := shareEventAt("2026-10-01T10:00:02Z", "chatgpt_web", CollectionMethodOTLP, "agent.response.completed", "agent")
	event.Event.ID = "browser-completion"
	event.Message = "chatgpt_web agent.response.completed"
	event.Content = &ContentInfo{Retention: ContentRetentionMetadata}
	builder := NewTraceBuilder("metadata-browser-turn", event)
	projected := builder.Add(event, TraceEventFromEvent(event, "", 1))
	bundle := builder.Bundle(TraceProvenanceV1{})
	if projected.Type != "agent_message" || projected.Actor != "assistant" || bundle.Summary.LocalMessageCount != 1 {
		t.Fatalf("browser completion lost its assistant identity: event=%+v summary=%+v", projected, bundle.Summary)
	}
	if messages := TraceMessages(bundle); len(messages) != 0 {
		t.Fatalf("metadata-only browser completion invented text: %+v", messages)
	}
}

func TestTraceEventTypeKeepsUnrelatedClassifications(t *testing.T) {
	for _, tc := range []struct{ action, category, want string }{
		{"prompt.submitted", "prompt", "user_message"},
		{"agent.reasoning", "agent", "agent_reasoning"},
		{"tool.invoked", "tool", "tool_call"},
		{"tool.completed", "tool", "tool_result"},
		{"command.executed", "command", "command"},
		{"file.read", "file", "file"},
		{"metric.observed", "metric", "other"},
		{"session.ended", "session", "session"},
		{"agent.unknown", "agent", "other"},
		{"AGENT.RESPONSE", "agent", "agent_message"},
		{" agent.response", "agent", "other"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			if got := TraceEventType(Event{Event: EventInfo{Action: tc.action, Category: tc.category}}); got != tc.want {
				t.Fatalf("type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeTraceEvent(t *testing.T) {
	metric := Event{Raw: map[string]interface{}{"metric_name": "claude_code.cost.usage"}}
	NormalizeTraceEvent(&metric)
	if metric.Event.Category != "metric" || metric.Event.Action != "claude_code.cost.usage" {
		t.Fatalf("metric = %+v", metric.Event)
	}
	file := Event{Event: EventInfo{Action: "file.read"}}
	NormalizeTraceEvent(&file)
	if file.Event.Category != "file" {
		t.Fatalf("category = %q", file.Event.Category)
	}
}

func TestTraceBuilderFoldsASummary(t *testing.T) {
	events := traceTestEvents()
	b := NewTraceBuilder("trace:abc", events[0])
	for i, event := range events {
		b.Add(event, TraceEventFromEvent(event, "line", i+10))
	}
	summary := b.Summary()
	if len(b.Events()) != 3 || b.Events()[2].Number != 3 {
		t.Fatalf("events = %+v", b.Events())
	}
	// The lifecycle event opens the trace, the prompt names it.
	if summary.Title != "explain the cache" || summary.Preview != "explain the cache" {
		t.Fatalf("title = %q, preview = %q", summary.Title, summary.Preview)
	}
	if summary.EventCount != 3 || summary.LocalMessageCount != 2 || summary.StartedAt != "2026-10-01T10:00:00Z" || summary.EndedAt != "2026-10-01T10:00:02Z" {
		t.Fatalf("counts = %+v", summary)
	}
	if summary.Trace == nil || summary.Trace.ID != "abc" || summary.Trace.RootSpanID != "root" {
		t.Fatalf("trace = %+v", summary.Trace)
	}
	if !reflect.DeepEqual(summary.Harness.CollectionMethods, []string{"hook", "plugin"}) || summary.Model == nil || summary.Model.Primary != "claude-sonnet-5-5" {
		t.Fatalf("harness = %+v, model = %+v", summary.Harness, summary.Model)
	}
	if summary.TokenUsage == nil || *summary.TokenUsage != (TraceUsageV1{InputTokens: 10, OutputTokens: 4}) {
		t.Fatalf("usage = %+v", summary.TokenUsage)
	}
	if summary.Repository == nil || summary.Repository.Branch != "main" || summary.Namespace == nil || summary.Namespace.Slug != "acme" {
		t.Fatalf("repository = %+v, namespace = %+v", summary.Repository, summary.Namespace)
	}
	if summary.Content.Retention != "full" || !summary.Content.HasRedactions {
		t.Fatalf("content = %+v", summary.Content)
	}
	// Nine remote events against three local ones: the share is behind.
	if summary.RemoteEventCount != 9 || summary.SharedEventCount != 9 || summary.Sharing.State != "stale" {
		t.Fatalf("sharing = %+v remote %d shared %d", summary.Sharing, summary.RemoteEventCount, summary.SharedEventCount)
	}
	// Summary does not change the builder.
	if again := b.Summary(); !reflect.DeepEqual(again, summary) {
		t.Fatalf("summary changed on a second read:\n%+v\n%+v", again, summary)
	}

	bundle := b.Bundle(TraceProvenanceV1{Source: "test"})
	if bundle.SchemaVersion != TraceSchemaVersion || bundle.ID != "trace:abc" || len(bundle.Events) != 3 || bundle.Range == nil || bundle.Range.TotalEvents != 3 {
		t.Fatalf("bundle = %+v", bundle)
	}
	if len(bundle.Spans) != 2 || bundle.Spans[0].ID != "child" || bundle.Spans[1].ID != "root" || bundle.Spans[1].Name != "Claude Code session started" {
		t.Fatalf("spans = %+v", bundle.Spans)
	}
}

// A builder restored from its saved state continues exactly as one that never stopped, which is
// what the dashboard's history relies on.
func TestTraceBuilderStateRoundTrip(t *testing.T) {
	events := traceTestEvents()
	whole := NewTraceBuilder("trace:abc", events[0])
	for i, event := range events {
		whole.Fold(event, TraceEventFromEvent(event, "", i+1))
	}
	first := NewTraceBuilder("trace:abc", events[0])
	first.Fold(events[0], TraceEventFromEvent(events[0], "", 1))
	data, err := json.Marshal(first.State())
	if err != nil {
		t.Fatal(err)
	}
	var state TraceBuilderState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	rest := RestoreTraceBuilder(state)
	for i, event := range events[1:] {
		rest.Fold(event, TraceEventFromEvent(event, "", i+2))
	}
	got, _ := json.Marshal(rest.Summary())
	want, _ := json.Marshal(whole.Summary())
	if string(got) != string(want) {
		t.Fatalf("restored summary differs:\n%s\n%s", got, want)
	}
	if len(rest.Events()) != 0 {
		t.Fatal("Fold kept events")
	}
}
