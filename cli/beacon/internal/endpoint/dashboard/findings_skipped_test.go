package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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

// TestFindingsAllStoreRulesSkippedStillRunsBaseline: the embedded baseline stays active
// alongside the store, so a store whose every rule needs a newer Beacon still scans with
// the baseline and names the skipped rules instead of failing.
func TestFindingsAllStoreRulesSkippedStillRunsBaseline(t *testing.T) {
	rec := findingsFixture(t, map[string]string{
		"future-a": storeRule("future-a", "threat-rules/v1.2"),
		"future-b": storeRule("future-b", "threat-rules/v2"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var resp FindingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	if len(resp.Skipped) != 2 || !strings.Contains(strings.Join(resp.Skipped, "\n"), "future-a.rule.yaml") {
		t.Fatalf("want both rules in skipped, got %v", resp.Skipped)
	}
}

// TestRunScanAllRulesSkippedNamesThem: with an explicit rules directory (no baseline),
// every rule needing a newer Beacon is skipped, and the NoRulesError carries them all.
func TestRunScanAllRulesSkippedNamesThem(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	dir := t.TempDir()
	for id, spec := range map[string]string{"future-a": "threat-rules/v1.2", "future-b": "threat-rules/v2"} {
		if err := os.WriteFile(filepath.Join(dir, id+".rule.yaml"), []byte(storeRule(id, spec)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunScan(true, logPath, dir, "", "")
	var noRules *detect.NoRulesError
	if !errors.As(err, &noRules) || len(noRules.Skipped) != 2 {
		t.Fatalf("want a NoRulesError naming both rules, got %v", err)
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
