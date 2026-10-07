package beaconevent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const summarizerBody = `{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"text","text":"\nWeb page content:\n---\nPAGE-CANARY\n---\n\nReturn the page text verbatim."},{"type":"tool_result","content":"file-private"}]},{"role":"assistant","content":[{"type":"text","text":"assistant-private"}]}],"system":[{"type":"text","text":"system-private"}],"tools":[{"name":"tool-private","description":"tool-description-private"}]}`

var privateBodyParts = []string{"system-private", "assistant-private", "file-private", "tool-private", "tool-description-private"}

func claudeBodyRecord(eventName, querySource, body string) plog.LogRecord {
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code." + eventName)
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1700000000, 0).UTC()))
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("service.version", "2.1.291")
	attrs.PutStr("event.name", eventName)
	attrs.PutStr("session.id", "s1")
	attrs.PutStr("model", "claude-opus-5-5")
	attrs.PutStr("request_body_id", "body-1")
	if querySource != "" {
		attrs.PutStr("query_source", querySource)
	}
	if body != "" {
		attrs.PutStr("body", body)
	}
	attrs.PutStr("body_ref", "/private/body.json")
	return record
}

// convertedLog runs record through EventsFromLogs, the path the exporters take, and reports whether
// it survived as an event.
func convertedLog(record plog.LogRecord) bool {
	logs := plog.NewLogs()
	record.CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())
	return len(NewConverter(Options{}).EventsFromLogs(logs)) == 1
}

func encodedEvent(t *testing.T, event Event) string {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(encoded)
}

func TestClaudeWebFetchSummarizerInputIsRecordedAsAWebFetchResult(t *testing.T) {
	record := claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, summarizerBody)
	if !convertedLog(record) {
		t.Fatal("the WebFetch summarizer request was dropped")
	}
	event := NewConverter(Options{}).EventFromLog(nil, record)

	if event.Event.Action != "tool.invoked" || event.Event.Category != "tool" {
		t.Fatalf("event = %s/%s, want tool.invoked/tool", event.Event.Action, event.Event.Category)
	}
	if event.Tool == nil || event.Tool.Name != "WebFetch" || !asymptoteobserve.IngestedContentToolName(event.Tool.Name) {
		t.Fatalf("tool = %#v, want WebFetch, which the rules engine reads as ingested content", event.Tool)
	}
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Name != "WebFetch" || event.GenAI.Tool.Call == nil {
		t.Fatalf("gen_ai.tool = %#v, want the WebFetch call", event.GenAI)
	}
	result, _ := event.GenAI.Tool.Call.Result.(string)
	if !strings.Contains(result, "PAGE-CANARY") || !strings.Contains(result, "Return the page text verbatim.") {
		t.Fatalf("result = %q, want the fetched page and its summarizer prompt", result)
	}
	// Third-party page text is never the user's prompt: not prompt.text, and not a user message.
	if event.Prompt != nil || event.GenAI.Input != nil {
		t.Fatalf("page text recorded as user input: prompt=%#v input=%#v", event.Prompt, event.GenAI.Input)
	}
	if event.Content == nil || !event.Content.Included || event.Content.Truncated || event.Content.Bytes != len(result) {
		t.Fatalf("content marker = %+v, want the retained result", event.Content)
	}
	if event.Model != "claude-opus-5-5" {
		t.Fatalf("model = %q, want the summarizer model", event.Model)
	}
	rawAttrs, _ := event.Raw["attributes"].(map[string]interface{})
	for _, key := range []string{"body", "body_ref", ClaudeWebFetchInputAttr} {
		if _, exists := rawAttrs[key]; exists {
			t.Fatalf("raw.attributes kept %q", key)
		}
	}
	if rawAttrs["query_source"] != claudeWebFetchApplyQuerySource {
		t.Fatalf("query_source provenance = %#v", rawAttrs["query_source"])
	}
	encoded := encodedEvent(t, event)
	if strings.Count(encoded, "PAGE-CANARY") != 1 {
		t.Fatalf("the page is stored %d times, want once: %s", strings.Count(encoded, "PAGE-CANARY"), encoded)
	}
	for _, forbidden := range append(privateBodyParts, "/private/body.json") {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("serialized event leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestClaudeAPIBodiesOtherThanTheSummarizerRequestAreDropped(t *testing.T) {
	cases := map[string]plog.LogRecord{
		"main request":                 claudeBodyRecord("api_request_body", "sdk", summarizerBody),
		"main response":                claudeBodyRecord("api_response_body", "sdk", `{"content":[{"type":"text","text":"private-body"}]}`),
		"summarizer response":          claudeBodyRecord("api_response_body", claudeWebFetchApplyQuerySource, `{"content":[{"type":"text","text":"private-body"}]}`),
		"no query_source":              claudeBodyRecord("api_request_body", "", summarizerBody),
		"summarizer, body in a file":   claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, ""),
		"summarizer, invalid JSON":     claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, `{"messages":[{"role":"user","content":"PARTIAL-PAGE`),
		"summarizer, trailing data":    claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, summarizerBody+`garbage`),
		"summarizer, two documents":    claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, summarizerBody+summarizerBody),
		"summarizer, no user text":     claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, `{"messages":[{"role":"assistant","content":"assistant-private"}]}`),
		"summarizer, top-level array":  claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, `[{"role":"user","content":"x"}]`),
		"summarizer, empty text block": claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, `{"messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`),
	}
	claimed := claudeBodyRecord("api_request_body", "sdk", "")
	claimed.Attributes().PutStr(ClaudeWebFetchInputAttr, "PAGE-CANARY")
	cases["main request claiming processed input"] = claimed
	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			if convertedLog(record) {
				t.Fatal("body event was kept")
			}
			// A caller that converts one record directly still gets no body.
			encoded := encodedEvent(t, NewConverter(Options{}).EventFromLog(nil, record))
			for _, forbidden := range append(privateBodyParts, "private-body", "PARTIAL-PAGE", "PAGE-CANARY", "/private/body.json") {
				if strings.Contains(encoded, forbidden) {
					t.Fatalf("converted event leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

// The policy follows the event name, so a record from another service name -- the Agent SDK -- or
// one that names itself only in the log body is held to it too.
func TestClaudeAPIBodyPolicyIsKeyedOnTheEventName(t *testing.T) {
	sdk := func(querySource string) plog.LogRecord {
		record := claudeBodyRecord("api_request_body", querySource, summarizerBody)
		record.Attributes().PutStr("service.name", "claude-agent-sdk")
		record.Attributes().Remove("event.name")
		return record
	}
	if convertedLog(sdk("sdk")) {
		t.Fatal("an Agent SDK main-loop body was kept")
	}
	event := NewConverter(Options{}).EventFromLog(nil, sdk(claudeWebFetchApplyQuerySource))
	if event.Harness.Name != "claude_agent_sdk" {
		t.Fatalf("harness = %q, want claude_agent_sdk", event.Harness.Name)
	}
	if event.Event.Action != "tool.invoked" || event.Tool == nil || event.Tool.Name != "WebFetch" {
		t.Fatalf("event = %s tool %#v, want the WebFetch result", event.Event.Action, event.Tool)
	}
	encoded := encodedEvent(t, event)
	for _, forbidden := range privateBodyParts {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("serialized event leaked %q", forbidden)
		}
	}
}

func TestClaudeTruncatedSummarizerBodyKeepsTheReadablePrefix(t *testing.T) {
	full := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"Web page content:\n---\nPAGE-START caf\u00e9 PAGE-MIDDLE\nPAGE-END\n---\nReturn the page text verbatim."}]}],"system":[{"type":"text","text":"system-private"}]}`
	cuts := map[string]string{
		"inside the text":       full[:strings.Index(full, "PAGE-MIDDLE")],
		"inside \\n":            full[:strings.Index(full, `PAGE-MIDDLE\n`)+len(`PAGE-MIDDLE\`)],
		"inside \\u00e9":        full[:strings.Index(full, `\u00e9`)+len(`\u00`)],
		"after the user turn":   full[:strings.Index(full, `"system"`)+len(`"sys`)],
		"inside the system key": full[:strings.Index(full, `system-private`)+len(`syst`)],
	}
	for name, body := range cuts {
		t.Run(name, func(t *testing.T) {
			record := claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, body)
			record.Attributes().PutStr("body_truncated", "true")
			if !convertedLog(record) {
				t.Fatal("a truncated summarizer request was dropped with its page prefix")
			}
			event := NewConverter(Options{}).EventFromLog(nil, record)
			result, _ := event.GenAI.Tool.Call.Result.(string)
			if !strings.HasPrefix(result, "Web page content:\n---\nPAGE-START caf") {
				t.Fatalf("result = %q, want the page prefix", result)
			}
			if strings.Contains(result, "system-private") {
				t.Fatalf("result took text from outside the user turn: %q", result)
			}
			if event.Content == nil || !event.Content.Truncated {
				t.Fatalf("content marker = %+v, want truncated", event.Content)
			}
		})
	}
	cutBeforeText := claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, full[:strings.Index(full, `"text":"Web`)])
	cutBeforeText.Attributes().PutBool("body_truncated", true)
	if convertedLog(cutBeforeText) {
		t.Fatal("a body cut before any user text was kept")
	}
}

func TestClaudeUserTextReadsEveryUserTextPartInAnyKeyOrder(t *testing.T) {
	body := `{"messages":[` +
		`{"content":"plain user text","role":"user"},` +
		`{"role":"assistant","content":"assistant-private"},` +
		`{"role":"user","content":[{"text":"block text","type":"text"},{"type":"image","source":{"data":"image-private"}},{"type":"tool_result","content":[{"type":"text","text":"file-private"}]}]}` +
		`]}`
	text, partial, ok := claudeUserText(body, false)
	if !ok || partial {
		t.Fatalf("ok=%t partial=%t, want a complete parse", ok, partial)
	}
	if text != "plain user text\nblock text" {
		t.Fatalf("text = %q", text)
	}
}

// splunk_hec forwards what the processor leaves, so the page leaves it redacted and capped the way
// the runtime log stores it, and converting with or without the processor records the same thing.
func TestClaudeWebFetchInputIsRedactedAndCappedOnEveryPath(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 32)
	page := "Web page content:\n---\nPAGE-CANARY api_key=" + secret + "\n" + strings.Repeat("x", 3*asymptoteobserve.DefaultRawStringLimit)
	body, err := json.Marshal(map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": page}}})
	if err != nil {
		t.Fatal(err)
	}
	direct := NewConverter(Options{}).EventFromLog(nil, claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, string(body)))

	record := claudeBodyRecord("api_request_body", claudeWebFetchApplyQuerySource, string(body))
	if SanitizeClaudeAPIBodyRecord(record) {
		t.Fatal("the processor dropped the summarizer request")
	}
	for _, key := range []string{"body", "body_ref"} {
		if _, exists := record.Attributes().Get(key); exists {
			t.Fatalf("the processor forwarded %q", key)
		}
	}
	value, _ := record.Attributes().Get(ClaudeWebFetchInputAttr)
	forwarded := value.Str()
	if !strings.Contains(forwarded, "PAGE-CANARY") || strings.Contains(forwarded, secret) || len(forwarded) > asymptoteobserve.DefaultRawStringLimit {
		t.Fatalf("forwarded input (%d bytes) = %.120q, want the page prefix, redacted and capped at %d", len(forwarded), forwarded, asymptoteobserve.DefaultRawStringLimit)
	}
	for _, key := range []string{ClaudeWebFetchInputTruncatedAttr, ClaudeWebFetchInputRedactedAttr} {
		if flag, ok := record.Attributes().Get(key); !ok || !flag.Bool() {
			t.Fatalf("the processor did not forward %s", key)
		}
	}
	processed := NewConverter(Options{}).EventFromLog(nil, record)
	for path, event := range map[string]Event{"direct": direct, "processed": processed} {
		if result, _ := event.GenAI.Tool.Call.Result.(string); result != forwarded {
			t.Fatalf("%s: stored result differs from what the processor forwards", path)
		}
		if event.Content == nil || !event.Content.Truncated || !event.Content.Redacted {
			t.Fatalf("%s: content marker = %+v, want truncated and redacted", path, event.Content)
		}
	}
	if *processed.Content != *direct.Content {
		t.Fatalf("processed marker %+v, direct marker %+v", processed.Content, direct.Content)
	}
}

func TestSanitizeClaudeAPIBodyRecordDropsOtherBodiesAndLeavesOtherRecords(t *testing.T) {
	if !SanitizeClaudeAPIBodyRecord(claudeBodyRecord("api_response_body", "sdk", `{}`)) {
		t.Fatal("the processor kept a main-loop body")
	}
	other := plog.NewLogRecord()
	other.Body().SetStr("claude_code.user_prompt")
	other.Attributes().PutStr("body", "not an API body")
	if SanitizeClaudeAPIBodyRecord(other) {
		t.Fatal("the processor dropped a record that is not an API body")
	}
	if value, _ := other.Attributes().Get("body"); value.Str() != "not an API body" {
		t.Fatal("the processor changed a record that is not an API body")
	}
}
