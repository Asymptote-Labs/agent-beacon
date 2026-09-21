package learning

import (
	"encoding/json"
	"fmt"
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
	if eval.EvaluatorModel != DefaultJevModel {
		t.Fatalf("model = %q", eval.EvaluatorModel)
	}
}

func TestEvaluatePostsSystemOneRequestToMockableEndpoint(t *testing.T) {
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
		trace, _ := req.State["trace"].(map[string]interface{})
		sawTrace = req.Model == "jev-test" && trace["trace_id"] == "trace-1" && len(req.Questions) == len(RubricQuestions)
		nine := 0.9
		seven := 0.7
		eight := 0.8
		_ = json.NewEncoder(w).Encode(jevResponse{
			Model: "jev-test-resolved",
			Answers: map[string]jevAnswer{
				"task_success":        {Type: "noul", Noul: &nine},
				"reusable_correction": {Type: "noul", Noul: &seven},
				"evidence_supported":  {Type: "noul", Noul: &eight},
			},
			Usage: &asymptoteobserve.LearningEvaluationUsageV1{InputTokens: 100, CostUSD: 0.0001},
		})
	}))
	defer server.Close()

	eval, err := Evaluate(t.Context(), EvaluatorOptions{Endpoint: server.URL, APIKey: "secret", Model: "jev-test"}, EvaluationInput{
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
	if eval.EvaluatorModel != "jev-test-resolved" || eval.Usage == nil || eval.CostEstimateUSD != 0.0001 {
		t.Fatalf("model/usage not retained: %#v", eval)
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

func TestProjectionKeepsHeadAndTailOfLongTraces(t *testing.T) {
	show := testTraceShow()
	show.Events = nil
	total := maxProjectionEvents * 3
	for i := 1; i <= total; i++ {
		show.Events = append(show.Events, asymptoteobserve.TraceEventV1{
			Number:  i,
			Type:    "tool",
			Action:  "tool.invoked",
			Summary: fmt.Sprintf("event %d", i),
		})
	}
	projection := BuildProjection(show)
	if len(projection.Events) != maxProjectionEvents+1 {
		t.Fatalf("projected events = %d, want %d", len(projection.Events), maxProjectionEvents+1)
	}
	if first := projection.Events[0].Summary; first != "event 1" {
		t.Fatalf("head dropped: first = %q", first)
	}
	if last := projection.Events[len(projection.Events)-1].Summary; last != fmt.Sprintf("event %d", total) {
		t.Fatalf("tail dropped: last = %q", last)
	}
}

func TestEvaluateRequiresAnswersOutsideDryRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{}}`))
	}))
	defer server.Close()
	_, err := Evaluate(t.Context(), EvaluatorOptions{Endpoint: server.URL}, EvaluationInput{Project: asymptoteobserve.LearningProjectV1{ID: "p"}, Trace: testTraceShow()})
	if err == nil || !strings.Contains(err.Error(), "answers") {
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
