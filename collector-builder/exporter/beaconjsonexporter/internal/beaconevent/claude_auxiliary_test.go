package beaconevent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestClaudeWebFetchAuxiliaryBodyRetainsOnlyUserText(t *testing.T) {
	body := `{"model":"claude-opus-5-5","system":[{"type":"text","text":"system-private"}],"messages":[{"role":"user","content":[{"type":"text","text":"Web page content:\n---\nPAGE-CANARY\n---\nReturn the page text verbatim."},{"type":"tool_result","content":"file-private"}]},{"role":"assistant","content":[{"type":"text","text":"assistant-private"}]}],"tools":[{"name":"tool-private","description":"tool-description-private"}]}`
	record := plog.NewLogRecord()
	record.Body().SetStr("claude_code.api_request_body")
	record.SetTimestamp(pcommonTimestamp(1700000000))
	attrs := record.Attributes()
	attrs.PutStr("service.name", "claude-code")
	attrs.PutStr("service.version", "2.1.291")
	attrs.PutStr("event.name", "api_request_body")
	attrs.PutStr("query_source", claudeWebFetchApplyQuerySource)
	attrs.PutStr("request_body_id", "webfetch-body-1")
	attrs.PutStr("model", "claude-opus-5-5")
	attrs.PutStr("body", body)
	attrs.PutStr("body_ref", "/private/request.json")

	event := NewConverter(Options{}).EventFromLog(nil, record)
	if event.Prompt == nil {
		t.Fatalf("auxiliary text was not promoted: %#v", event)
	}
	text := event.Prompt.Text
	if !strings.Contains(text, "PAGE-CANARY") || !strings.Contains(text, "Return the page text verbatim.") {
		t.Fatalf("typed auxiliary text = %q, want WebFetch page and its prompt", text)
	}
	for _, forbidden := range []string{"system-private", "assistant-private", "file-private", "tool-private", "tool-description-private"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("typed auxiliary text includes unrelated %q: %q", forbidden, text)
		}
	}
	if event.Event.Action != "session.activity" || event.Prompt == nil || event.Prompt.Text != text {
		t.Fatalf("auxiliary event = action %q prompt %#v, want session.activity with typed input text", event.Event.Action, event.Prompt)
	}
	if event.Content == nil || !event.Content.Included || event.Content.Hash == "" || event.Content.Bytes != len(text) {
		t.Fatalf("content marker = %+v, want retained typed auxiliary text", event.Content)
	}
	if event.Model != "claude-opus-5-5" {
		t.Fatalf("model provenance = %q, want Claude model", event.Model)
	}
	rawAttrs, ok := event.Raw["attributes"].(map[string]interface{})
	if !ok {
		t.Fatalf("raw attributes = %#v", event.Raw)
	}
	if _, exists := rawAttrs["body"]; exists {
		t.Fatal("raw API body was retained")
	}
	if _, exists := rawAttrs["body_ref"]; exists {
		t.Fatal("raw API body file reference was retained")
	}
	if got := rawAttrs["query_source"]; got != claudeWebFetchApplyQuerySource {
		t.Fatalf("query_source provenance = %#v, want %q", got, claudeWebFetchApplyQuerySource)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, forbidden := range []string{"system-private", "assistant-private", "file-private", "tool-private", "tool-description-private", "/private/request.json"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("serialized event leaked unrelated body data %q", forbidden)
		}
	}
}

func TestClaudeOtherAPIBodiesAreNotPromotedOrRetained(t *testing.T) {
	for _, name := range []string{"api_request_body", "api_response_body"} {
		t.Run(name, func(t *testing.T) {
			record := plog.NewLogRecord()
			record.Body().SetStr("claude_code." + name)
			attrs := record.Attributes()
			attrs.PutStr("service.name", "claude-code")
			attrs.PutStr("event.name", name)
			attrs.PutStr("query_source", "main")
			attrs.PutStr("body", `{"messages":[{"role":"user","content":"private-body"}]}`)
			attrs.PutStr("body_ref", "/private/body.json")
			event := NewConverter(Options{}).EventFromLog(nil, record)
			if event.Prompt != nil {
				t.Fatalf("unselected body promoted as auxiliary text: %#v", event.Prompt)
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-body") || strings.Contains(string(encoded), "/private/body.json") {
				t.Fatalf("unselected API body or reference was retained: %s", encoded)
			}
		})
	}
}

func TestClaudeWebFetchUnavailableBodyDoesNotRetainPartialContentOrReference(t *testing.T) {
	for _, body := range []string{"", `{"messages":[{"role":"user","content":"PARTIAL-PAGE`} {
		t.Run(body, func(t *testing.T) {
			record := plog.NewLogRecord()
			record.Body().SetStr("claude_code.api_request_body")
			attrs := record.Attributes()
			attrs.PutStr("service.name", "claude-code")
			attrs.PutStr("event.name", "api_request_body")
			attrs.PutStr("query_source", claudeWebFetchApplyQuerySource)
			attrs.PutStr("body", body)
			attrs.PutStr("body_ref", "/private/unread-request.json")
			event := NewConverter(Options{}).EventFromLog(nil, record)
			if event.Prompt != nil || event.Content != nil {
				t.Fatalf("unavailable body became retained input: prompt=%#v content=%#v", event.Prompt, event.Content)
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "PARTIAL-PAGE") || strings.Contains(string(encoded), "/private/unread-request.json") {
				t.Fatalf("unavailable body or reference leaked: %s", encoded)
			}
		})
	}
}

func pcommonTimestamp(seconds int64) pcommon.Timestamp {
	return pcommon.Timestamp(uint64(time.Unix(seconds, 0).UnixNano()))
}
