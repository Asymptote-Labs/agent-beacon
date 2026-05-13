package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestReadEventsSkipsMalformedLinesAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path,
		testEvent("2026-05-13T01:00:00Z", "cursor", "command.executed", "command", "repo-a"),
		[]byte("{not json"),
		testEvent("2026-05-13T01:01:00Z", "cursor", "file.modified", "file", "repo-b"),
	)

	result, err := ReadEvents(path, EventQuery{Action: "command.executed", Limit: 10})
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}
	if result.MalformedLines != 1 {
		t.Fatalf("MalformedLines = %d, want 1", result.MalformedLines)
	}
	if result.TotalMatched != 1 || len(result.Events) != 1 {
		t.Fatalf("matched events = total %d len %d, want 1/1", result.TotalMatched, len(result.Events))
	}
	if result.Events[0].Event.Event.Action != "command.executed" {
		t.Fatalf("unexpected event action: %s", result.Events[0].Event.Event.Action)
	}
}

func TestBuildSummaryAggregatesSignals(t *testing.T) {
	result := EventResult{
		TotalMatched: 2,
		Events: []EventRecord{
			{Event: schema.Event{
				Timestamp: "2026-05-13T01:00:00Z",
				Event:     schema.EventInfo{Action: "command.executed", Category: "command"},
				Severity:  schema.SeverityInfo,
				Harness:   schema.HarnessInfo{Name: "cursor"},
				Session:   &schema.SessionInfo{ID: "s1"},
				Command:   &schema.CommandInfo{Command: "go test ./..."},
			}},
			{Event: schema.Event{
				Timestamp: "2026-05-13T01:01:00Z",
				Event:     schema.EventInfo{Action: "mcp.tool_invoked", Category: "mcp"},
				Severity:  schema.SeverityHigh,
				Harness:   schema.HarnessInfo{Name: "cursor"},
				Session:   &schema.SessionInfo{ID: "s2"},
				MCP:       &schema.MCPInfo{Server: "github", Tool: "get_issue"},
			}},
		},
	}

	summary := BuildSummary(result)
	if summary.TotalEvents != 2 || summary.ActiveSessions != 2 {
		t.Fatalf("summary totals = events %d sessions %d, want 2/2", summary.TotalEvents, summary.ActiveSessions)
	}
	if summary.CommandEvents != 1 || summary.MCPEvents != 1 {
		t.Fatalf("signal counts = command %d mcp %d, want 1/1", summary.CommandEvents, summary.MCPEvents)
	}
	if summary.CountsByHarness["cursor"] != 2 {
		t.Fatalf("cursor harness count = %d, want 2", summary.CountsByHarness["cursor"])
	}
}

func TestHandlerEventsEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, testEvent("2026-05-13T01:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a"))

	handler, err := Handler(Options{LogPath: path, UserMode: true})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/events?action=prompt.submitted", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var result EventResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(result.Events))
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	if err := ValidateLoopbackAddr("127.0.0.1:8765"); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
	if err := ValidateLoopbackAddr("0.0.0.0:8765"); err == nil {
		t.Fatal("expected non-loopback address to be rejected")
	}
}

func writeTestLog(t *testing.T, path string, lines ...[]byte) {
	t.Helper()
	var data []byte
	for _, line := range lines {
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test log: %v", err)
	}
}

func testEvent(ts, harness, action, category, repo string) []byte {
	event := schema.Event{
		Timestamp:     ts,
		Vendor:        schema.Vendor,
		Product:       schema.Product,
		SchemaVersion: schema.SchemaVersion,
		Event:         schema.EventInfo{Kind: "agent_runtime", Action: action, Category: category},
		Severity:      schema.SeverityInfo,
		Endpoint:      schema.EndpointInfo{OS: "darwin"},
		Harness:       schema.HarnessInfo{Name: harness},
		Repository:    repo,
		Message:       action,
	}
	data, _ := json.Marshal(event)
	return data
}
