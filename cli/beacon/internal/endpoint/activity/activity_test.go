package activity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestSearchReturnsTruncationSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := testEvent("2026-05-13T01:00:00Z", "claude", "prompt.submitted", "prompt")
	event.Prompt = &schema.PromptInfo{Text: "summarize local MCP use"}
	event.Truncated = true
	writeLog(t, path, event)

	result, err := Search(Query{LogPath: path, Harness: "claude", Limit: 10})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result.Meta.TotalMatched != 1 || len(result.Events) != 1 {
		t.Fatalf("matched events = total %d len %d, want 1/1", result.Meta.TotalMatched, len(result.Events))
	}
	got := result.Events[0]
	if got.ID == "" || got.Action != "prompt.submitted" || got.Harness != "claude" {
		t.Fatalf("unexpected event summary: %#v", got)
	}
	if got.Content == nil || !got.Content.FieldTruncated {
		t.Fatalf("content summary = %#v, want field truncation marker", got.Content)
	}
	if len(got.Caveats) != 1 || got.Caveats[0] != "content was truncated" {
		t.Fatalf("truncation caveat = %#v, want content was truncated", got.Caveats)
	}
}

func TestListFiltersIncludesRotatedArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	active := testEvent("2026-05-13T01:02:00Z", "cursor", "prompt.submitted", "prompt")
	active.Repository = "repo-active"
	archive := testEvent("2026-05-13T01:00:00Z", "claude", "mcp.tool_invoked", "mcp")
	archive.MCP = &schema.MCPInfo{Server: "github", Tool: "get_issue"}
	writeLog(t, path, active)
	writeLog(t, path+".1", archive)

	result, err := ListFilters(Query{LogPath: path, Limit: 10})
	if err != nil {
		t.Fatalf("ListFilters returned error: %v", err)
	}
	if !hasValue(result.Filters["harnesses"], "claude") || !hasValue(result.Filters["mcp_servers"], "github") {
		t.Fatalf("filters missing archive values: %#v", result.Filters)
	}
}

func TestGetEventReturnsNotFound(t *testing.T) {
	result, err := GetEvent(filepath.Join(t.TempDir(), "runtime.jsonl"), "line-1")
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if result.Found {
		t.Fatal("Found = true, want false")
	}
}

func TestGetEventRequiresLogPath(t *testing.T) {
	if _, err := GetEvent("", "line-1"); err == nil {
		t.Fatal("GetEvent accepted empty log path")
	}
}

func TestListFiltersAddsWindowCaveatWhenTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLog(t, path,
		testEvent("2026-05-13T01:00:00Z", "cursor", "prompt.submitted", "prompt"),
		testEvent("2026-05-13T01:01:00Z", "claude", "prompt.submitted", "prompt"),
	)
	result, err := ListFilters(Query{LogPath: path, Limit: 1})
	if err != nil {
		t.Fatalf("ListFilters returned error: %v", err)
	}
	if !result.Meta.Truncated || len(result.Meta.Caveats) == 0 {
		t.Fatalf("meta = %#v, want truncated caveat", result.Meta)
	}
}

func TestSearchReportsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := testEvent("2026-05-13T01:00:00Z", "cursor", "command.executed", "command")
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(data, '\n'), []byte("{not json\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := Search(Query{LogPath: path, Limit: 10})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result.Meta.MalformedLines != 1 || len(result.Meta.Caveats) == 0 {
		t.Fatalf("malformed meta = %#v, want malformed caveat", result.Meta)
	}
}

func hasValue(values []ValueCount, want string) bool {
	for _, value := range values {
		if value.Value == want {
			return true
		}
	}
	return false
}

func writeLog(t *testing.T, path string, events ...schema.Event) {
	t.Helper()
	var data []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func testEvent(ts, harness, action, category string) schema.Event {
	return schema.Event{
		Timestamp:     ts,
		Vendor:        schema.Vendor,
		Product:       schema.Product,
		SchemaVersion: schema.SchemaVersion,
		Event:         schema.EventInfo{Kind: "agent_runtime", Action: action, Category: category},
		Severity:      schema.SeverityInfo,
		Endpoint:      schema.EndpointInfo{OS: "darwin"},
		Harness:       schema.HarnessInfo{Name: harness},
		Message:       action,
	}
}
