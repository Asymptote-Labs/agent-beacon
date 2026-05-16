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

func TestReadEventsInfersMissingCategory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := testSchemaEvent("2026-05-13T01:00:00Z", "codex_cli", "tool.invoked", "", "repo-a")
	writeTestLog(t, path, marshalEvents(t, event)...)

	result, err := ReadEvents(path, EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events length = %d, want 1", len(result.Events))
	}
	if got := result.Events[0].Event.Event.Category; got != "tool" {
		t.Fatalf("Category = %q, want tool", got)
	}
}

func TestReadEventsNormalizesMetricWithMissingAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := testSchemaEvent("2026-05-13T01:00:00Z", "cli", "", "metric", "")
	event.Message = "droid.hook.invocations"
	writeTestLog(t, path, marshalEvents(t, event)...)

	result, err := ReadEvents(path, EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events length = %d, want 1", len(result.Events))
	}
	if got := result.Events[0].Event.Event.Action; got != "droid.hook.invocations" {
		t.Fatalf("Action = %q, want droid.hook.invocations", got)
	}
}

func TestReadEventsPromotesRawMetricName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := testSchemaEvent("2026-05-13T01:00:00Z", "cli", "metric.observed", "metric", "")
	event.Message = "fallback.metric"
	event.Raw = map[string]interface{}{"metric_name": "droid.tool.execution_time"}
	writeTestLog(t, path, marshalEvents(t, event)...)

	result, err := ReadEvents(path, EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events length = %d, want 1", len(result.Events))
	}
	if got := result.Events[0].Event.Event.Action; got != "droid.tool.execution_time" {
		t.Fatalf("Action = %q, want droid.tool.execution_time", got)
	}
}

func TestBuildSummaryAggregatesSignals(t *testing.T) {
	result := EventResult{
		TotalMatched: 4,
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
			}, WazuhLevel: WazuhLevel("mcp.tool_invoked")},
			{Event: schema.Event{
				Timestamp: "2026-05-13T01:02:00Z",
				Event:     schema.EventInfo{Action: "approval.denied", Category: "approval"},
				Severity:  schema.SeverityMedium,
				Harness:   schema.HarnessInfo{Name: "cursor"},
				Session:   &schema.SessionInfo{ID: "s3"},
				Approval:  &schema.ApprovalInfo{Decision: "denied"},
			}, WazuhLevel: WazuhLevel("approval.denied")},
			{Event: schema.Event{
				Timestamp:  "2026-05-13T01:03:00Z",
				Event:      schema.EventInfo{Action: "policy.blocked", Category: "approval"},
				Severity:   schema.SeverityCritical,
				Harness:    schema.HarnessInfo{Name: "cursor"},
				Repository: "repo-a",
				Policy:     &schema.PolicyInfo{Decision: "blocked", Name: "dangerous command"},
			}},
		},
	}

	summary := BuildSummary(result)
	if summary.TotalEvents != 4 || summary.ActiveSessions != 3 {
		t.Fatalf("summary totals = events %d sessions %d, want 4/3", summary.TotalEvents, summary.ActiveSessions)
	}
	if summary.CommandEvents != 1 || summary.MCPEvents != 1 {
		t.Fatalf("signal counts = command %d mcp %d, want 1/1", summary.CommandEvents, summary.MCPEvents)
	}
	if summary.CountsByHarness["cursor"] != 4 {
		t.Fatalf("cursor harness count = %d, want 4", summary.CountsByHarness["cursor"])
	}
	if summary.NeedsReviewEvents != 3 || summary.DeniedApprovalEvents != 1 || summary.PolicyBlockedEvents != 1 {
		t.Fatalf("review counts = needs %d denied %d blocked %d, want 3/1/1", summary.NeedsReviewEvents, summary.DeniedApprovalEvents, summary.PolicyBlockedEvents)
	}
}

func TestReadEventsFreeTextSearchMatchesStructuredFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	events := []schema.Event{
		testSchemaEvent("2026-05-13T01:00:00Z", "cursor", "command.executed", "command", "repo-a"),
		testSchemaEvent("2026-05-13T01:01:00Z", "cursor", "file.modified", "file", "repo-b"),
		testSchemaEvent("2026-05-13T01:02:00Z", "cursor", "mcp.tool_invoked", "mcp", "repo-c"),
		testSchemaEvent("2026-05-13T01:03:00Z", "cursor", "approval.denied", "approval", "repo-d"),
		testSchemaEvent("2026-05-13T01:04:00Z", "cursor", "prompt.submitted", "prompt", "repo-e"),
	}
	events[0].Command = &schema.CommandInfo{Command: "go test ./internal/endpoint/dashboard"}
	events[0].Session = &schema.SessionInfo{ID: "session-command"}
	events[1].File = &schema.FileInfo{Path: "cmd/server.go", Operation: "write", Language: "go"}
	events[2].MCP = &schema.MCPInfo{Server: "github", Tool: "get_issue"}
	events[3].Approval = &schema.ApprovalInfo{Decision: "denied", Reason: "dangerous shell"}
	events[3].Message = "approval blocked for review"
	events[4].Prompt = &schema.PromptInfo{Text: "summarize local telemetry"}
	writeTestLog(t, path, marshalEvents(t, events...)...)

	for _, query := range []string{"dashboard", "cmd/server.go", "github get_issue", "dangerous shell", "session-command", "repo-c", "blocked review", "summarize telemetry"} {
		result, err := ReadEvents(path, EventQuery{Q: query, Limit: 10})
		if err != nil {
			t.Fatalf("ReadEvents(%q) returned error: %v", query, err)
		}
		if result.TotalMatched != 1 {
			t.Fatalf("ReadEvents(%q) matched %d events, want 1", query, result.TotalMatched)
		}
	}
}

func TestReadEventsSecurityFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	events := []schema.Event{
		testSchemaEvent("2026-05-13T01:00:00Z", "cursor", "command.executed", "command", "repo-a"),
		testSchemaEvent("2026-05-13T01:01:00Z", "cursor", "mcp.tool_invoked", "mcp", "repo-b"),
		testSchemaEvent("2026-05-13T01:02:00Z", "cursor", "approval.denied", "approval", "repo-c"),
		testSchemaEvent("2026-05-13T01:03:00Z", "cursor", "policy.blocked", "approval", "repo-d"),
	}
	events[0].Severity = schema.SeverityHigh
	events[1].MCP = &schema.MCPInfo{Server: "github", Tool: "get_issue"}
	events[2].Approval = &schema.ApprovalInfo{Decision: "denied", Reason: "blocked command"}
	events[3].Policy = &schema.PolicyInfo{Name: "shell guard", Decision: "blocked", Reason: "unsafe"}
	writeTestLog(t, path, marshalEvents(t, events...)...)

	cases := []struct {
		name  string
		query EventQuery
		want  string
	}{
		{name: "severity", query: EventQuery{Severity: "high", Limit: 10}, want: "command.executed"},
		{name: "category", query: EventQuery{Category: "mcp", Limit: 10}, want: "mcp.tool_invoked"},
		{name: "mcp", query: EventQuery{MCP: "github", Limit: 10}, want: "mcp.tool_invoked"},
		{name: "decision", query: EventQuery{Decision: "denied", Limit: 10}, want: "approval.denied"},
		{name: "policy", query: EventQuery{Policy: "shell guard", Limit: 10}, want: "policy.blocked"},
		{name: "wazuh", query: EventQuery{WazuhLevel: "10", Policy: "shell guard", Limit: 10}, want: "policy.blocked"},
		{name: "review", query: EventQuery{Review: "true", Action: "tool.failed", Limit: 10}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ReadEvents(path, tc.query)
			if err != nil {
				t.Fatalf("ReadEvents returned error: %v", err)
			}
			if tc.want == "" {
				if result.TotalMatched != 0 {
					t.Fatalf("matched %d events, want 0", result.TotalMatched)
				}
				return
			}
			if result.TotalMatched != 1 || result.Events[0].Event.Event.Action != tc.want {
				t.Fatalf("matched %d action %q, want 1 %q", result.TotalMatched, result.Events[0].Event.Event.Action, tc.want)
			}
		})
	}
}

func TestFindEventCanReadOutsideTailWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := make([][]byte, 0, maxEventLimit+1)
	lines = append(lines, testEvent("2026-05-13T01:00:00Z", "cursor", "command.executed", "command", "repo-a"))
	for i := 0; i < maxEventLimit; i++ {
		lines = append(lines, testEvent("2026-05-13T01:01:00Z", "cursor", "file.modified", "file", "repo-b"))
	}
	writeTestLog(t, path, lines...)

	record, ok, err := FindEvent(path, "line-1")
	if err != nil {
		t.Fatalf("FindEvent returned error: %v", err)
	}
	if !ok || record.Event.Event.Action != "command.executed" {
		t.Fatalf("FindEvent returned ok=%t action=%q, want line-1 command.executed", ok, record.Event.Event.Action)
	}
}

func TestHandlerEventEndpointOmitsRawAndSetsSecurityHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, testEvent("2026-05-13T01:00:00Z", "cursor", "prompt.submitted", "prompt", "repo-a"))

	handler, err := Handler(Options{LogPath: path, UserMode: true})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/event?id=line-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("Content-Security-Policy header missing")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := response["raw"]; ok {
		t.Fatal("event response unexpectedly included raw JSONL bytes")
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
	event := testSchemaEvent(ts, harness, action, category, repo)
	data, _ := json.Marshal(event)
	return data
}

func testSchemaEvent(ts, harness, action, category, repo string) schema.Event {
	return schema.Event{
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
}

func marshalEvents(t *testing.T, events ...schema.Event) [][]byte {
	t.Helper()
	lines := make([][]byte, 0, len(events))
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		lines = append(lines, data)
	}
	return lines
}
