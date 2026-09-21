package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
)

func TestMemoryEvaluationCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"memory", "evaluations", "run"},
		{"memory", "evaluations", "list"},
		{"memory", "evaluations", "show"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd == nil {
			t.Fatalf("command %v not registered: %v", path, err)
		}
	}
}

func TestMemoryEvaluationsRunDryRunPreviewsCost(t *testing.T) {
	logPath, project := writeMemoryCommandFixture(t)
	resetMemoryOpts(t)
	memoryOpts.userMode = true
	memoryOpts.logPath = logPath
	memoryOpts.projectPath = project
	memoryOpts.traceID = "session:cursor:s1"
	memoryOpts.dryRun = true
	memoryOpts.jsonOutput = true
	memoryOpts.jevCost = 0.01

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryEvaluationsRun(cmd, nil); err != nil {
		t.Fatalf("runMemoryEvaluationsRun returned error: %v", err)
	}
	var result evaluationRunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out.String())
	}
	if !result.DryRun || result.TraceCount != 1 || result.EstimatedCostUSD != 0.01 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(learning.PathForRuntimeLog(logPath)); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create memory store, stat err=%v", err)
	}
}

func TestMemoryEvaluationsRunPersistsMockedJevResult(t *testing.T) {
	logPath, project := writeMemoryCommandFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"questions":[{"id":"task_success","probability":0.9},{"id":"reusable_correction","probability":0.8},{"id":"evidence_supported","probability":0.7}]}`))
	}))
	defer server.Close()

	resetMemoryOpts(t)
	memoryOpts.userMode = true
	memoryOpts.logPath = logPath
	memoryOpts.projectPath = project
	memoryOpts.traceID = "session:cursor:s1"
	memoryOpts.jsonOutput = true
	memoryOpts.jevEndpoint = server.URL
	memoryOpts.jevAPIKey = "test-key"
	memoryOpts.jevCost = learning.DefaultCostPerTrace

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryEvaluationsRun(cmd, nil); err != nil {
		t.Fatalf("runMemoryEvaluationsRun returned error: %v", err)
	}
	var result evaluationRunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out.String())
	}
	if len(result.Evaluations) != 1 || result.Evaluations[0].Score < 0.79 || result.Evaluations[0].Score > 0.81 {
		t.Fatalf("evaluations = %#v", result.Evaluations)
	}

	memoryOpts.query = "session:cursor:s1"
	list, err := memoryStore().ListEvaluations(learning.Query{ProjectID: result.Project.ID, Q: "cursor"})
	if err != nil {
		t.Fatalf("ListEvaluations: %v", err)
	}
	if len(list) != 1 || !strings.HasPrefix(list[0].ID, "eval_") {
		t.Fatalf("persisted evaluations = %#v", list)
	}
}

func writeMemoryCommandFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "endpoint", "logs", "runtime.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	event := schema.Event{
		Timestamp:     "2026-09-21T01:00:00Z",
		Vendor:        schema.Vendor,
		Product:       schema.Product,
		SchemaVersion: schema.SchemaVersion,
		Event:         schema.EventInfo{ID: "event-1", Kind: "agent_runtime", Action: "prompt.submitted", Category: "prompt"},
		Severity:      schema.SeverityInfo,
		Endpoint:      schema.EndpointInfo{OS: "darwin"},
		Harness:       schema.HarnessInfo{Name: "cursor"},
		Session:       &schema.SessionInfo{ID: "s1", WorkingDirectory: project},
		Prompt:        &schema.PromptInfo{Text: "Fix flaky package smoke"},
		Message:       "Fix flaky package smoke",
		Repository:    project,
	}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(line, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	return logPath, project
}

func resetMemoryOpts(t *testing.T) {
	t.Helper()
	previous := memoryOpts
	memoryOpts = struct {
		userMode    bool
		systemMode  bool
		logPath     string
		jsonOutput  bool
		projectPath string
		limit       int
		page        int
		query       string
		harness     string
		traceID     string
		since       string
		until       string
		dryRun      bool
		jevEndpoint string
		jevAPIKey   string
		jevCost     float64
		timeout     time.Duration
	}{userMode: true, limit: 25, page: 1, jevCost: learning.DefaultCostPerTrace}
	t.Cleanup(func() { memoryOpts = previous })
}
