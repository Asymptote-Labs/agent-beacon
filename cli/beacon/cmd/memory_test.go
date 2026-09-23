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
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMemoryEvaluationCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"memory", "evaluations", "run"},
		{"memory", "evaluations", "list"},
		{"memory", "evaluations", "show"},
		{"memory", "candidates", "list"},
		{"memory", "candidates", "show"},
		{"memory", "candidates", "approve"},
		{"memory", "candidates", "reject"},
		{"memory", "candidates", "supersede"},
		{"memory", "skills", "preview"},
		{"memory", "skills", "install"},
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
	memoryOpts.jevModel = "jev-test"
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
	if len(result.Candidates) != 1 || result.Candidates[0].State != "candidate" {
		t.Fatalf("candidates = %#v", result.Candidates)
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

func TestMemoryCandidatesApproveCommand(t *testing.T) {
	logPath, project := writeMemoryCommandFixture(t)
	resetMemoryOpts(t)
	memoryOpts.userMode = true
	memoryOpts.logPath = logPath
	memoryOpts.projectPath = project
	candidate := testCommandCandidate(t)
	if err := memoryStore().PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	memoryOpts.reason = "reviewed"
	memoryOpts.jsonOutput = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryCandidatesApprove(cmd, []string{candidate.ID}); err != nil {
		t.Fatalf("runMemoryCandidatesApprove returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"memory"`) || !strings.Contains(out.String(), `"approved"`) {
		t.Fatalf("unexpected approve output: %s", out.String())
	}
}

func TestMemorySkillsPreviewAndInstallCommands(t *testing.T) {
	logPath, project := writeMemoryCommandFixture(t)
	resetMemoryOpts(t)
	memoryOpts.userMode = true
	memoryOpts.logPath = logPath
	memoryOpts.projectPath = project
	candidate, memory := testApprovedCommandMemory()
	store := memoryStore()
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(memory); err != nil {
		t.Fatal(err)
	}

	var preview bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&preview)
	if err := runMemorySkillsPreview(cmd, []string{candidate.ID}); err != nil {
		t.Fatalf("runMemorySkillsPreview returned error: %v", err)
	}
	if !strings.Contains(preview.String(), "beacon_memory_id") || !strings.Contains(preview.String(), "Retry package smoke") {
		t.Fatalf("preview output = %s", preview.String())
	}

	var install bytes.Buffer
	cmd.SetOut(&install)
	if err := runMemorySkillsInstall(cmd, []string{candidate.ID}); err != nil {
		t.Fatalf("runMemorySkillsInstall returned error: %v", err)
	}
	if !strings.Contains(install.String(), ".agents") {
		t.Fatalf("install output = %s", install.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "beacon-debugging-pattern-retry-package-smoke", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
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
		jevModel    string
		jevCost     float64
		timeout     time.Duration
		state       string
		kind        string
		reason      string
		replacement string
		force       bool
	}{userMode: true, limit: 25, page: 1, jevEndpoint: learning.DefaultJevEndpoint, jevModel: learning.DefaultJevModel, jevCost: learning.DefaultCostPerTrace}
	t.Cleanup(func() { memoryOpts = previous })
}

func testCommandCandidate(t *testing.T) asymptoteobserve.LearningCandidateV1 {
	t.Helper()
	candidate, ok := learning.CandidateFromEvaluation(asymptoteobserve.LearningEvaluationV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		ID:            "eval-command",
		Status:        asymptoteobserve.LearningEvaluationStatusCompleted,
		Score:         0.9,
		Project:       asymptoteobserve.LearningProjectV1{ID: "project-1"},
		Trace: asymptoteobserve.LearningTraceRefV1{
			ID:       "trace-command",
			Title:    "Fix flaky package smoke",
			Harness:  asymptoteobserve.TraceHarnessV1{Name: "cursor"},
			EventIDs: []string{"event-1"},
		},
		Questions: []asymptoteobserve.LearningEvaluationQuestionV1{
			{ID: "task_success", Probability: 0.9},
			{ID: "reusable_correction", Probability: 0.9},
			{ID: "evidence_supported", Probability: 0.9},
		},
	})
	if !ok {
		t.Fatal("candidate not created")
	}
	return candidate
}

func testApprovedCommandMemory() (asymptoteobserve.LearningCandidateV1, asymptoteobserve.LearningMemoryV1) {
	project := asymptoteobserve.LearningProjectV1{ID: "project-1"}
	evidence := []asymptoteobserve.LearningEvidenceV1{{TraceID: "trace-1", Summary: "Retry fixed a transient failure"}}
	candidate := asymptoteobserve.LearningCandidateV1{
		ID:       "candidate-approved",
		MemoryID: "memory-approved",
		State:    asymptoteobserve.LearningCandidateStateApproved,
		Kind:     asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:    "Debugging pattern: Retry package smoke",
		Body:     "Rerun package smoke once after confirming the error is transient.",
		Project:  project,
		Evidence: evidence,
	}
	memory := asymptoteobserve.LearningMemoryV1{
		ID:            candidate.MemoryID,
		CandidateID:   candidate.ID,
		Kind:          candidate.Kind,
		Title:         candidate.Title,
		Body:          candidate.Body,
		Applicability: "when package smoke fails transiently",
		Tags:          []string{"beacon", "debugging_pattern"},
		Project:       project,
		Evidence:      evidence,
	}
	return candidate, memory
}

// Issue #620: evaluations stored by earlier releases carry reason "noul", the Jev
// question type, not a rationale. The text view must not present it as one.
func TestMemoryEvaluationsShowHidesPlaceholderReason(t *testing.T) {
	logPath, project := writeMemoryCommandFixture(t)
	resetMemoryOpts(t)
	memoryOpts.userMode = true
	memoryOpts.logPath = logPath
	memoryOpts.projectPath = project
	eval := asymptoteobserve.LearningEvaluationV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		ID:            "eval-placeholder",
		Status:        asymptoteobserve.LearningEvaluationStatusCompleted,
		Score:         0.7,
		Project:       asymptoteobserve.LearningProjectV1{ID: "project-1"},
		Trace:         asymptoteobserve.LearningTraceRefV1{ID: "trace-placeholder"},
		Questions: []asymptoteobserve.LearningEvaluationQuestionV1{
			{ID: "task_success", Probability: 0.43, Reason: "noul"},
			{ID: "reusable_correction", Probability: 0.78, Reason: "Retry isolates the flaky step."},
		},
	}
	if err := memoryStore().PutEvaluation(eval); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryEvaluationsShow(cmd, []string{eval.ID}); err != nil {
		t.Fatalf("runMemoryEvaluationsShow returned error: %v", err)
	}
	if strings.Contains(out.String(), "noul") {
		t.Fatalf("show printed the placeholder reason:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Retry isolates the flaky step.") {
		t.Fatalf("show dropped a real reason:\n%s", out.String())
	}
}
