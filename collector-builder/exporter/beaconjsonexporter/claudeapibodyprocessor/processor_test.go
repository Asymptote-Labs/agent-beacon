package claudeapibodyprocessor

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/processor/processortest"
)

const summarizerBody = `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"Web page content:\n---\nPAGE-CANARY\n---"}]}],"system":[{"type":"text","text":"system-private"}]}`

func appendBody(records plog.LogRecordSlice, eventName, querySource, body string) {
	record := records.AppendEmpty()
	record.Body().SetStr("claude_code." + eventName)
	record.Attributes().PutStr("event.name", eventName)
	record.Attributes().PutStr("query_source", querySource)
	record.Attributes().PutStr("body", body)
	record.Attributes().PutStr("body_ref", "/private/body.json")
}

// Every exporter downstream sees what this processor forwards, so what it forwards is the whole
// policy: no API body, no body file path, and the summarizer input under its own attribute.
func TestProcessorForwardsOnlyTheSummarizerInput(t *testing.T) {
	sink := new(consumertest.LogsSink)
	proc, err := NewFactory().CreateLogs(context.Background(), processortest.NewNopSettings(componentType), &Config{}, sink)
	if err != nil {
		t.Fatal(err)
	}

	logs := plog.NewLogs()
	records := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	appendBody(records, "api_request_body", "sdk", `{"system":"system-private"}`)
	appendBody(records, "api_response_body", "web_fetch_apply", `{"content":"summary-private"}`)
	appendBody(records, "api_request_body", "web_fetch_apply", summarizerBody)
	prompt := records.AppendEmpty()
	prompt.Body().SetStr("claude_code.user_prompt")
	prompt.Attributes().PutStr("prompt", "user prompt")
	onlyBodies := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
	appendBody(onlyBodies, "api_response_body", "sdk", `{"content":"response-private"}`)

	if err := proc.ConsumeLogs(context.Background(), logs); err != nil {
		t.Fatal(err)
	}
	got := sink.AllLogs()
	if len(got) != 1 || got[0].LogRecordCount() != 2 || got[0].ResourceLogs().Len() != 1 {
		t.Fatalf("forwarded %d batches, want one resource with the summarizer request and the prompt", len(got))
	}
	forwarded := got[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	summarizer := forwarded.At(0).Attributes()
	for _, key := range []string{"body", "body_ref"} {
		if _, exists := summarizer.Get(key); exists {
			t.Fatalf("forwarded %q", key)
		}
	}
	if input, ok := summarizer.Get("beacon.web_fetch.input"); !ok || !strings.Contains(input.Str(), "PAGE-CANARY") || strings.Contains(input.Str(), "system-private") {
		t.Fatalf("summarizer input = %v, want the user text only", input.AsRaw())
	}
	if forwarded.At(1).Body().Str() != "claude_code.user_prompt" {
		t.Fatalf("the prompt record was not forwarded unchanged")
	}

	empty := plog.NewLogs()
	appendBody(empty.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords(), "api_request_body", "sdk", `{}`)
	if err := proc.ConsumeLogs(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	if len(sink.AllLogs()) != 1 {
		t.Fatal("a batch with nothing left in it was forwarded")
	}
}
