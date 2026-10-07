package beaconevent

import (
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const mainRequestPrivateText = "D01_FILE_CONTENT_MUST_NOT_BE_RETAINED"

func mainRequestBody(description string) string {
	tools, _ := json.Marshal([]map[string]interface{}{
		{"name": "Read", "description": "Read a local file"},
		{"name": "mcp__notes__save_note", "description": description, "input_schema": map[string]interface{}{"type": "object"}},
	})
	return `{"model":"claude-model","messages":[{"role":"user","content":"` + mainRequestPrivateText + `"}],"system":[{"type":"text","text":"system-private"}],"tools":` + string(tools) + `}`
}

func listingsOf(events []Event) []Event {
	var listed []Event
	for _, event := range events {
		if event.Event.Action == "mcp.tool_listed" {
			listed = append(listed, event)
		}
	}
	return listed
}

// Bodies a user turned on without the opt-in list no MCP tools, in the converter or the processor.
func TestClaudeRequestBodyListsNoMCPToolWithoutTheOptIn(t *testing.T) {
	logs := plog.NewLogs()
	claudeBodyRecord("api_request_body", "sdk", mainRequestBody("Saves a note.")).CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())
	if events := NewConverter(Options{}).EventsFromLogs(logs); len(events) != 0 {
		t.Fatalf("events = %v, want none without the opt-in", actionsOf(events))
	}
	if !SanitizeClaudeAPIBodyRecord(claudeBodyRecord("api_request_body", "sdk", mainRequestBody("Saves a note.")), false, &MCPListings{}) {
		t.Fatal("without the opt-in the processor kept a request body that advertised an MCP tool")
	}
}

// A main-conversation request body becomes one mcp.tool_listed per MCP tool it advertised, in place
// of the request's own event, and nothing else of the body survives.
func TestClaudeRequestBodyBecomesOneListingPerMCPTool(t *testing.T) {
	record := claudeBodyRecord("api_request_body", "sdk", mainRequestBody("Saves a note. Tool description marker C03."))
	logs := plog.NewLogs()
	record.CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())

	events := NewConverter(captureModelContext).EventsFromLogs(logs)
	if len(events) != 1 || events[0].Event.Action != "mcp.tool_listed" {
		t.Fatalf("events = %v, want only the MCP tool's listing", actionsOf(events))
	}
	listed := events[0]
	if listed.MCP == nil || listed.MCP.Server != "notes" || listed.MCP.Tool != "save_note" {
		t.Fatalf("mcp = %#v, want notes/save_note", listed.MCP)
	}
	if listed.GenAI.Tool.Name != "mcp__notes__save_note" || listed.GenAI.Tool.Description != "Saves a note. Tool description marker C03." {
		t.Fatalf("gen_ai.tool = %#v, want the advertised name and description", listed.GenAI.Tool)
	}
	if listed.Session == nil || listed.Session.ID != "s1" {
		t.Fatalf("session = %#v, want s1", listed.Session)
	}
	encoded := encodedEvent(t, listed)
	for _, private := range []string{mainRequestPrivateText, "system-private", "Read a local file", "/private/body.json", `"body"`} {
		if strings.Contains(encoded, private) {
			t.Fatalf("listing retained %q from the request body: %s", private, encoded)
		}
	}
}

func TestClaudeRequestBodyWithoutMCPToolsIsDropped(t *testing.T) {
	body := `{"model":"claude-model","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","description":"Read a local file"}]}`
	if convertedLog(claudeBodyRecord("api_request_body", "sdk", body)) {
		t.Fatal("a request body with no MCP tools was kept")
	}
}

func TestClaudeRequestTruncationKeepsOnlyCompleteDefinitions(t *testing.T) {
	body := `{"tools":[{"name":"mcp__notes__complete","description":"Complete description","input_schema":{}},{"name":"mcp__notes__partial","description":"truncated"`
	got := extractClaudeMCPToolDescriptions(body)
	if len(got) != 1 || got[0].Name != "mcp__notes__complete" || got[0].Description != "Complete description" {
		t.Fatalf("definitions = %#v, want only the fully decoded tool preceding truncation", got)
	}
}

// Every destination is sent the same copy, so the description is redacted and capped before it
// leaves the body, at the limit gen_ai.tool.description is stored at.
func TestClaudeMCPToolDescriptionsAreRedactedAndCapped(t *testing.T) {
	long := "Saves a note. token=sk-live-0123456789abcdef " + strings.Repeat("padding ", 1000)
	tools := claudeMCPTools(mainRequestBody(long))
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want the one MCP tool", tools)
	}
	if strings.Contains(tools[0].Description, "sk-live-0123456789abcdef") {
		t.Fatalf("description kept the secret: %q", tools[0].Description[:80])
	}
	if len(tools[0].Description) > asymptoteobserve.DefaultStringLimit {
		t.Fatalf("description = %d bytes, want at most %d", len(tools[0].Description), asymptoteobserve.DefaultStringLimit)
	}
}

// The processor sends a session each tool once, and again only when its description changes. What
// it forwards converts to the same listing the converter makes from the body itself.
func TestProcessorForwardsEachMCPToolOncePerSessionAndDescription(t *testing.T) {
	listings := &MCPListings{}
	send := func(session, description string) (plog.LogRecord, bool) {
		record := claudeBodyRecord("api_request_body", "sdk", mainRequestBody(description))
		record.Attributes().PutStr("session.id", session)
		return record, !SanitizeClaudeAPIBodyRecord(record, true, listings)
	}

	first, kept := send("s1", "Saves a note.")
	if !kept {
		t.Fatal("the first request advertising the tool was dropped")
	}
	for _, key := range []string{"body", "body_ref"} {
		if _, ok := first.Attributes().Get(key); ok {
			t.Fatalf("forwarded record still carries %s", key)
		}
	}
	if _, kept := send("s1", "Saves a note."); kept {
		t.Fatal("a repeat of the session's listing was forwarded")
	}
	if _, kept := send("s1", "Saves a note. Now poisoned."); !kept {
		t.Fatal("a changed description was not forwarded")
	}
	if _, kept := send("s2", "Saves a note."); !kept {
		t.Fatal("another session's first listing was not forwarded")
	}

	logs := plog.NewLogs()
	first.CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())
	fromProcessor := listingsOf(NewConverter(captureModelContext).EventsFromLogs(logs))
	direct := listingsOf(NewConverter(captureModelContext).EventsFromLogs(func() plog.Logs {
		logs := plog.NewLogs()
		claudeBodyRecord("api_request_body", "sdk", mainRequestBody("Saves a note.")).CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())
		return logs
	}()))
	if len(fromProcessor) != 1 || len(direct) != 1 || encodedEvent(t, fromProcessor[0]) != encodedEvent(t, direct[0]) {
		t.Fatalf("processed record converted to %v, want the converter's own %v", fromProcessor, direct)
	}
}

// The stored description is the redacted, capped copy, so a listing's content marker says when
// Beacon changed it -- by the converter alone or through the processor -- and not otherwise.
func TestClaudeMCPListingMarksADescriptionBeaconRedactedOrCut(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 32)
	padded := "Saves a note. api_key=" + secret + " " + strings.Repeat("x", 2*asymptoteobserve.DefaultStringLimit)
	logsOf := func(record plog.LogRecord) plog.Logs {
		logs := plog.NewLogs()
		record.CopyTo(logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty())
		return logs
	}
	listed := func(t *testing.T, description string, viaProcessor bool) Event {
		t.Helper()
		record := claudeBodyRecord("api_request_body", "sdk", mainRequestBody(description))
		if viaProcessor && SanitizeClaudeAPIBodyRecord(record, true, &MCPListings{}) {
			t.Fatal("the processor dropped the request")
		}
		events := listingsOf(NewConverter(captureModelContext).EventsFromLogs(logsOf(record)))
		if len(events) != 1 || events[0].Content == nil {
			t.Fatalf("listings = %v, want one with a content marker", actionsOf(events))
		}
		return events[0]
	}
	for _, viaProcessor := range []bool{false, true} {
		event := listed(t, padded, viaProcessor)
		if description := event.GenAI.Tool.Description; strings.Contains(description, secret) || len(description) > asymptoteobserve.DefaultStringLimit {
			t.Fatalf("processor=%t: stored description (%d bytes) kept the secret or the padding", viaProcessor, len(description))
		}
		if !event.Content.Truncated || !event.Content.Redacted {
			t.Fatalf("processor=%t: content = %+v, want truncated and redacted", viaProcessor, event.Content)
		}
		if plain := listed(t, "Saves a note.", viaProcessor); plain.Content.Truncated || plain.Content.Redacted {
			t.Fatalf("processor=%t: an untouched description is marked %+v", viaProcessor, plain.Content)
		}
	}
}

func actionsOf(events []Event) []string {
	var actions []string
	for _, event := range events {
		actions = append(actions, event.Event.Action)
	}
	return actions
}
