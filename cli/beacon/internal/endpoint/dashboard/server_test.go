package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/tokens"
)

func TestStatusUsesExplicitRuntimeLogPath(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var status StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if status.LogPath != logPath {
		t.Fatalf("LogPath = %q, want %q", status.LogPath, logPath)
	}
	if status.RuntimeLog.EffectiveLogPath != logPath {
		t.Fatalf("RuntimeLog.EffectiveLogPath = %q, want %q", status.RuntimeLog.EffectiveLogPath, logPath)
	}
}

func TestTokensEndpointAggregatesUsage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := []string{
		// Claude Code metric datapoint events (delta temporality).
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"local-session"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":100}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"local-session"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"output_tokens":40}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`,
		`{"timestamp":"2026-06-11T10:05:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"cost.usage","category":"metric"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"local-session"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"cost_usd":0.5}},"message":"claude_code.cost.usage","raw":{"metric_name":"claude_code.cost.usage","metric_temporality":"Delta"}}`,
		// Cloud SDK span usage with trace identity.
		`{"timestamp":"2026-06-11T11:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"tool.invoked","category":"tool"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"asymptote_observe"},"origin":"cloud","session":{"id":"cloud-session"},"trace":{"id":"trace-1","span_id":"span-1"},"model":"gpt-4o-mini","gen_ai":{"usage":{"input_tokens":60,"output_tokens":20}},"message":"agent.step"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tokens?bucket=1h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report tokens.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal tokens report: %v", err)
	}
	if report.Totals.InputTokens != 160 || report.Totals.OutputTokens != 60 || report.Totals.CostUSD != 0.5 {
		t.Fatalf("totals = %#v", report.Totals)
	}
	if len(report.ByModel) != 2 || report.ByModel[0].Key != "claude-sonnet-4-5" {
		t.Fatalf("by_model = %#v", report.ByModel)
	}
	if len(report.BySession) != 2 {
		t.Fatalf("by_session = %#v", report.BySession)
	}
	if len(report.Series) != 2 {
		t.Fatalf("series = %#v", report.Series)
	}

	// Session filter plus per-step detail for the cloud session.
	req = httptest.NewRequest(http.MethodGet, "/api/tokens?session=cloud-session", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	report = tokens.Report{}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal session tokens report: %v", err)
	}
	if report.Totals.InputTokens != 60 {
		t.Fatalf("session totals = %#v", report.Totals)
	}
	if report.SessionDetail == nil || report.SessionDetail.SessionID != "cloud-session" || len(report.SessionDetail.Steps) != 1 {
		t.Fatalf("session detail = %#v", report.SessionDetail)
	}
	if report.SessionDetail.Steps[0].SpanID != "span-1" || report.SessionDetail.Steps[0].Usage.InputTokens != 60 {
		t.Fatalf("session step = %#v", report.SessionDetail.Steps[0])
	}
}

// TestTokensEndpointDedupesCumulativeSameSecondDatapoints reproduces a real
// Claude Code export: a batch of cumulative token.usage datapoints written to
// the log within the same second. ReadEvents returns events newest-first, so
// the token endpoint must still feed Aggregate in chronological order or the
// cumulative dedup misreads each step-down as a counter reset and sums the raw
// cumulative values instead of the per-interval deltas.
func TestTokensEndpointDedupesCumulativeSameSecondDatapoints(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	mk := func(ts string, cumulative int64) string {
		return `{"timestamp":"` + ts + `","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":` +
			itoa(cumulative) + `}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Cumulative"}}`
	}
	// Append order is chronological; all three share the same second.
	lines := []string{
		mk("2026-06-11T10:00:00Z", 1200),
		mk("2026-06-11T10:00:00Z", 2700),
		mk("2026-06-11T10:00:00Z", 4500),
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tokens", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report tokens.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal tokens report: %v", err)
	}
	// Deltas 1200, 1500, 1800 -> 4500. Raw (broken) sum would be 8400.
	if report.Totals.InputTokens != 4500 {
		t.Fatalf("cumulative input tokens = %d, want 4500 (raw over-count is 8400)", report.Totals.InputTokens)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestStaticDashboardPagesServe(t *testing.T) {
	handler, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	cases := []struct {
		path string
		want string
	}{
		{path: "/", want: "Beacon Endpoint Log Search"},
		{path: "/overview.html", want: "Beacon Endpoint Security Overview"},
		{path: "/tokens.html", want: "Beacon Endpoint Token Usage"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body did not contain %q", tc.want)
			}
		})
	}
}
