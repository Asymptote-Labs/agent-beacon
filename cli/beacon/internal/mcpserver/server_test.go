package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestServeStdioListsAndCallsTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, testEvent("2026-05-13T01:00:00Z", "cursor", "prompt.submitted", "prompt"))
	server := New(Options{LogPath: path})
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_activity","arguments":{"harness":"cursor","limit":5}}}`,
		"",
	}, "\n")
	var output bytes.Buffer
	if err := server.ServeStdio(t.Context(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("ServeStdio returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("response lines = %d, want 2; output=%s", len(lines), output.String())
	}
	var list map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if !strings.Contains(lines[0], "search_activity") {
		t.Fatalf("tools/list missing search_activity: %s", lines[0])
	}
	if !strings.Contains(lines[1], "prompt.submitted") {
		t.Fatalf("tools/call missing event result: %s", lines[1])
	}
}

func TestHTTPHandlerHealthAndMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeTestLog(t, path, testEvent("2026-05-13T01:00:00Z", "cursor", "prompt.submitted", "prompt"))
	handler := New(Options{LogPath: path}).HTTPHandler()

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", health.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"summarize_activity","arguments":{}}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "total_events") {
		t.Fatalf("mcp response missing summary: %s", rec.Body.String())
	}
}

func TestExpectedToolsRegistered(t *testing.T) {
	server := New(Options{LogPath: "runtime.jsonl"})
	if err := server.HasExpectedTools(); err != nil {
		t.Fatalf("HasExpectedTools returned error: %v", err)
	}
	if got := strings.Join(server.ToolNames(), ","); got != "search_activity,summarize_activity,get_activity_event,list_activity_filters" {
		t.Fatalf("ToolNames = %s", got)
	}
}

func TestValidateTransport(t *testing.T) {
	for _, tc := range []struct {
		transport string
		wantErr   bool
	}{
		{"", false},
		{TransportStdio, false},
		{TransportHTTP, false},
		{"sse", true},
		{"grpc", true},
	} {
		err := ValidateTransport(tc.transport)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateTransport(%q) error = %v, wantErr %v", tc.transport, err, tc.wantErr)
		}
	}
}

func writeTestLog(t *testing.T, path string, events ...schema.Event) {
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
