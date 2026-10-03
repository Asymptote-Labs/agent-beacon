package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

// storeRule is a minimal valid rule; spec, when set, is written as its spec line.
func storeRule(id, spec string) string {
	specLine := ""
	if spec != "" {
		specLine = "spec: " + spec + "\n"
	}
	return "id: " + id + "\n" + specLine + `version: 1
title: T
severity: high
status: experimental
posture: detect
match: 'e.event.action == "file.read"'
emit:
  reason: read
tests:
  - name: p
    verdict: match
    events:
      - event: { action: file.read }
`
}

// findingsFixture writes the given rules into a fresh user-mode store and a one-event
// runtime log, and returns the /api/findings response.
func findingsFixture(t *testing.T, rules map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	store := detect.StoreDir(true)
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, body := range rules {
		if err := os.WriteFile(filepath.Join(store, id+".rule.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	line := `{"timestamp":"2026-06-11T10:00:00Z","vendor":"beacon","product":"endpoint-agent","schema_version":"1.0","event":{"kind":"agent_runtime","action":"file.read","category":"file"},"severity":"info","endpoint":{"os":"darwin"},"session":{"id":"s1"},"file":{"path":".env"},"message":"file.read"}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
	return rec
}

func TestFindingsAllRulesSkippedNamesThem(t *testing.T) {
	rec := findingsFixture(t, map[string]string{
		"future-a": storeRule("future-a", "threat-rules/v1.2"),
		"future-b": storeRule("future-b", "threat-rules/v2"),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string   `json:"error"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	if strings.Contains(body.Error, "store is empty") {
		t.Fatalf("error must not claim the store is empty: %s", body.Error)
	}
	for _, want := range []string{
		`rule "future-a" requires threat-rules/v1.2`,
		`rule "future-b" requires threat-rules/v2`,
		"supports up to " + threatrules.SupportedSpec,
		"upgrade Beacon",
	} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("error %q missing %q", body.Error, want)
		}
	}
	if len(body.Skipped) != 2 || !strings.Contains(strings.Join(body.Skipped, "\n"), "future-a.rule.yaml") {
		t.Fatalf("want both rules in skipped, got %v", body.Skipped)
	}
}

func TestFindingsSomeRulesSkippedRunsTheRest(t *testing.T) {
	rec := findingsFixture(t, map[string]string{
		"current-rule": storeRule("current-rule", ""),
		"future-rule":  storeRule("future-rule", "threat-rules/v1.2"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp FindingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || resp.Findings[0].RuleID != "current-rule" {
		t.Fatalf("want the current rule's finding, got %+v", resp.Findings)
	}
	if len(resp.Skipped) != 1 || !strings.Contains(resp.Skipped[0], `rule "future-rule" requires threat-rules/v1.2`) {
		t.Fatalf("want future-rule in skipped, got %v", resp.Skipped)
	}
}

func TestFindingsNothingSkippedOmitsSkipList(t *testing.T) {
	rec := findingsFixture(t, map[string]string{"current-rule": storeRule("current-rule", "")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"skipped"`) {
		t.Fatalf("skipped must be omitted when nothing is skipped: %s", rec.Body.String())
	}
}
