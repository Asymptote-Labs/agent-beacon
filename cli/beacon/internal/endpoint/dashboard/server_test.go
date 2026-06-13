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

// TestTokensEndpointDedupesManySameSecondDatapoints uses more than nine
// cumulative datapoints in the same second so line numbers reach two digits.
// ReadEvents breaks same-timestamp ties on lexicographic line IDs (line-9 >
// line-10), so the token path must reorder by numeric append order or the
// cumulative dedup scrambles and inflates totals.
func TestTokensEndpointDedupesManySameSecondDatapoints(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	var lines []string
	for k := int64(1); k <= 12; k++ {
		// Cumulative input k*100 -> per-interval delta of 100 each -> 1200 total.
		lines = append(lines, `{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"s1"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":`+itoa(k*100)+`}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Cumulative"}}`)
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
	if report.Totals.InputTokens != 1200 {
		t.Fatalf("cumulative input tokens = %d, want 1200", report.Totals.InputTokens)
	}
}

// TestTokensEndpointSessionFilterIsExact guards against substring session
// matching: filtering session-1 must not fold in session-10's usage.
func TestTokensEndpointSessionFilterIsExact(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	mk := func(session string, input int64) string {
		return `{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"` + session + `"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":` + itoa(input) + `}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`
	}
	lines := []string{mk("session-1", 100), mk("session-10", 999)}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tokens?session=session-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report tokens.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal tokens report: %v", err)
	}
	if report.Totals.InputTokens != 100 {
		t.Fatalf("session-1 totals = %d, want 100 (session-10 must not fold in)", report.Totals.InputTokens)
	}
	if report.SessionDetail == nil || report.SessionDetail.Usage.InputTokens != 100 {
		t.Fatalf("session detail = %#v, want exact session-1 usage", report.SessionDetail)
	}
}

// TestTokensEndpointSessionFilterIsCaseInsensitive guards the token session
// filter against case mismatches: the event query matches sessions
// case-insensitively, so the post-filter and drilldown must too, or a
// differently-cased filter value returns zero usage for matching events.
func TestTokensEndpointSessionFilterIsCaseInsensitive(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"token.usage","category":"metric"},"severity":"info","endpoint":{"os":"linux"},"harness":{"name":"claude_code"},"session":{"id":"Session-ABC"},"model":"claude-sonnet-4-5","gen_ai":{"usage":{"input_tokens":123}},"message":"claude_code.token.usage","raw":{"metric_name":"claude_code.token.usage","metric_temporality":"Delta"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tokens?session=session-abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report tokens.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal tokens report: %v", err)
	}
	if report.Totals.InputTokens != 123 {
		t.Fatalf("totals = %d, want 123 for case-insensitive session match", report.Totals.InputTokens)
	}
	if report.SessionDetail == nil || report.SessionDetail.Usage.InputTokens != 123 {
		t.Fatalf("session detail = %#v, want case-insensitive match", report.SessionDetail)
	}
}

func TestDetectionsEndpointListsBaselineRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // empty store -> embedded baseline
	handler, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/detections", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp DetectionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal detections: %v", err)
	}
	if resp.Count == 0 || resp.Count != len(resp.Rules) {
		t.Fatalf("count = %d, rules = %d", resp.Count, len(resp.Rules))
	}
	for _, rule := range resp.Rules {
		if rule.Source != "baseline" {
			t.Fatalf("rule %q source = %q, want baseline", rule.ID, rule.Source)
		}
		if rule.ID == "" || rule.Severity == "" || rule.Kind == "" {
			t.Fatalf("rule missing fields: %#v", rule)
		}
	}
}

func TestFindingsEndpointReturnsHitsLinkedToRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // empty store -> embedded baseline
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	// A destructive command the baseline "recursive-root-delete" rule matches.
	line := `{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"command.executed","category":"command"},"severity":"info","endpoint":{"os":"darwin"},"harness":{"name":"claude_code"},"session":{"id":"local-session"},"command":{"command":"rm -rf /"},"message":"command.executed"}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0644); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/findings", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp FindingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal findings: %v", err)
	}
	if resp.Scanned != 1 {
		t.Fatalf("scanned = %d, want 1", resp.Scanned)
	}
	found := false
	for _, f := range resp.Findings {
		if f.RuleID == "recursive-root-delete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a recursive-root-delete finding, got %#v", resp.Findings)
	}

	// An out-of-range min_severity is rejected.
	bad := httptest.NewRequest(http.MethodGet, "/api/findings?min_severity=nope", nil)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid min_severity status = %d, want 400", badRec.Code)
	}
}

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
		{path: "/detections.html", want: "Beacon Endpoint Detections"},
		{path: "/findings.html", want: "Beacon Endpoint Findings"},
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
