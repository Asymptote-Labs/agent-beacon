package learning

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestEvaluateDryRunDoesNotNeedEndpoint(t *testing.T) {
	show := testTraceShow()
	project := asymptoteobserve.LearningProjectV1{ID: "project-1"}
	eval, err := Evaluate(t.Context(), EvaluatorOptions{}, EvaluationInput{Project: project, Trace: show, DryRun: true})
	if err != nil {
		t.Fatalf("Evaluate dry-run returned error: %v", err)
	}
	if eval.Status != asymptoteobserve.LearningEvaluationStatusDryRun {
		t.Fatalf("status = %s", eval.Status)
	}
	if eval.ID == "" || eval.RubricHash == "" || eval.CostEstimateUSD == 0 {
		t.Fatalf("dry-run missing identifiers/cost: %#v", eval)
	}
}

func TestEvaluatePostsProjectionToMockableEndpoint(t *testing.T) {
	var sawAuth bool
	var sawTrace bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer secret" {
			sawAuth = true
		}
		var req jevRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		sawTrace = req.Trace.TraceID == "trace-1" && len(req.Trace.Questions) == len(RubricQuestions)
		_ = json.NewEncoder(w).Encode(jevResponse{
			Questions: []asymptoteobserve.LearningEvaluationQuestionV1{
				{ID: "task_success", Probability: 0.9, Confidence: 0.8},
				{ID: "reusable_correction", Probability: 0.7, Confidence: 0.6},
				{ID: "evidence_supported", Probability: 0.8, Confidence: 0.9},
			},
		})
	}))
	defer server.Close()

	eval, err := Evaluate(t.Context(), EvaluatorOptions{Endpoint: server.URL, APIKey: "secret"}, EvaluationInput{
		Project: asymptoteobserve.LearningProjectV1{ID: "project-1"},
		Trace:   testTraceShow(),
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if !sawAuth || !sawTrace {
		t.Fatalf("endpoint did not see expected request auth=%v trace=%v", sawAuth, sawTrace)
	}
	if eval.Status != asymptoteobserve.LearningEvaluationStatusCompleted {
		t.Fatalf("status = %s", eval.Status)
	}
	if eval.Score <= 0.79 || eval.Score >= 0.81 {
		t.Fatalf("score = %f", eval.Score)
	}
}

func TestProjectionRedactsAndBoundsContent(t *testing.T) {
	show := testTraceShow()
	show.Events[0].Content = &asymptoteobserve.TraceContentV1{Text: "token=supersecretvalue " + strings.Repeat("a", maxProjectionText*2)}
	projection := BuildProjection(show)
	if len(projection.Events) != 1 {
		t.Fatalf("events = %d", len(projection.Events))
	}
	content := projection.Events[0].Content
	if strings.Contains(content, "supersecretvalue") {
		t.Fatalf("projection leaked secret: %s", content)
	}
	if len(content) > maxProjectionText+32 {
		t.Fatalf("projection content not bounded: %d", len(content))
	}
}

func TestEvaluateRequiresEndpointOutsideDryRun(t *testing.T) {
	_, err := Evaluate(t.Context(), EvaluatorOptions{}, EvaluationInput{Project: asymptoteobserve.LearningProjectV1{ID: "p"}, Trace: testTraceShow()})
	if err == nil || !strings.Contains(err.Error(), "Jev endpoint is required") {
		t.Fatalf("error = %v", err)
	}
}

func testTraceShow() asymptoteobserve.TraceShowResultV1 {
	return asymptoteobserve.TraceShowResultV1{
		Trace: asymptoteobserve.TraceSummaryV1{
			ID:      "trace-1",
			Title:   "Fix flaky package smoke",
			Harness: asymptoteobserve.TraceHarnessV1{Name: "claude_code"},
		},
		Events: []asymptoteobserve.TraceEventV1{
			{
				ID:      "event-1",
				Number:  1,
				Type:    "command",
				Action:  "command.executed",
				Title:   "Run package smoke",
				Summary: "Package smoke failed once, then passed after retry.",
				Content: &asymptoteobserve.TraceContentV1{Text: "npm test passed"},
			},
		},
		Range: asymptoteobserve.TraceRangeV1{TotalEvents: 1, ReturnedEvents: 1},
	}
}
