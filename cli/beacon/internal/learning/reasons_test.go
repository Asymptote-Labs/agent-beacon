package learning

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// evaluateWithJevBody runs Evaluate against a local stub that answers with body.
func evaluateWithJevBody(t *testing.T, body string) asymptoteobserve.LearningEvaluationV1 {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	eval, err := Evaluate(t.Context(), EvaluatorOptions{Endpoint: server.URL}, EvaluationInput{
		Project: asymptoteobserve.LearningProjectV1{ID: "project-1"},
		Trace:   testTraceShow(),
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	return eval
}

func questionByID(t *testing.T, eval asymptoteobserve.LearningEvaluationV1, id string) asymptoteobserve.LearningEvaluationQuestionV1 {
	t.Helper()
	for _, question := range eval.Questions {
		if question.ID == id {
			return question
		}
	}
	t.Fatalf("question %s missing from %#v", id, eval.Questions)
	return asymptoteobserve.LearningEvaluationQuestionV1{}
}

// Issue #620: the answer's question type ("noul") was stored as the reason.
func TestEvaluateDoesNotReportAnswerTypeAsReason(t *testing.T) {
	eval := evaluateWithJevBody(t, `{"answers":{
		"task_success":{"type":"noul","noul":0.43},
		"reusable_correction":{"type":"noul","noul":0.78},
		"evidence_supported":{"type":"noul","noul":0.49}}}`)
	for _, question := range eval.Questions {
		if question.Reason != "" {
			t.Fatalf("question %s reason = %q, want empty when Jev returns no rationale", question.ID, question.Reason)
		}
	}
	if got := questionByID(t, eval, "reusable_correction").Probability; got != 0.78 {
		t.Fatalf("reusable_correction probability = %v, want 0.78", got)
	}
}

func TestEvaluateKeepsJevAnswerReason(t *testing.T) {
	eval := evaluateWithJevBody(t, `{"answers":{
		"task_success":{"type":"noul","noul":0.9,"reason":"  The final test run\n\tpassed after the retry.  "},
		"reusable_correction":{"type":"noul","noul":0.8,"rationale":"Retrying the smoke once isolates the flaky network step."},
		"evidence_supported":{"type":"noul","noul":0.7,"explanation":"Event 1 shows the failure and the passing rerun."}}}`)
	cases := map[string]string{
		"task_success":        "The final test run passed after the retry.",
		"reusable_correction": "Retrying the smoke once isolates the flaky network step.",
		"evidence_supported":  "Event 1 shows the failure and the passing rerun.",
	}
	for id, want := range cases {
		if got := questionByID(t, eval, id).Reason; got != want {
			t.Fatalf("question %s reason = %q, want %q", id, got, want)
		}
	}
}

func TestEvaluateRedactsAndBoundsJevReason(t *testing.T) {
	long := strings.Repeat("b", maxProjectionText*2)
	eval := evaluateWithJevBody(t, `{"answers":{
		"task_success":{"type":"noul","noul":0.9,"reason":"token=supersecretvalue `+long+`"}}}`)
	reason := questionByID(t, eval, "task_success").Reason
	if strings.Contains(reason, "supersecretvalue") {
		t.Fatalf("reason leaked secret: %s", reason)
	}
	if !strings.Contains(reason, "bbbbbbbb") || len(reason) > maxProjectionText+32 {
		t.Fatalf("reason not bounded: %d bytes", len(reason))
	}
}

func TestEvaluateDropsPlaceholderReasonFromLegacyShape(t *testing.T) {
	eval := evaluateWithJevBody(t, `{"questions":[
		{"id":"task_success","probability":0.9,"reason":"noul"},
		{"id":"reusable_correction","probability":0.8,"reason":"Pin the retry to the network step."},
		{"id":"evidence_supported","probability":0.7}]}`)
	if got := questionByID(t, eval, "task_success").Reason; got != "" {
		t.Fatalf("placeholder reason kept: %q", got)
	}
	if got := questionByID(t, eval, "reusable_correction").Reason; got != "Pin the retry to the network step." {
		t.Fatalf("real reason = %q", got)
	}
}

func TestQuestionReasonHidesPlaceholder(t *testing.T) {
	for _, value := range []string{"", "   ", "noul", " NOUL "} {
		if got := QuestionReason(asymptoteobserve.LearningEvaluationQuestionV1{Reason: value}); got != "" {
			t.Fatalf("QuestionReason(%q) = %q, want empty", value, got)
		}
	}
	if got := QuestionReason(asymptoteobserve.LearningEvaluationQuestionV1{Reason: " real reason "}); got != "real reason" {
		t.Fatalf("QuestionReason trimmed = %q", got)
	}
}

func TestCandidateBodyIncludesEvaluatorReasons(t *testing.T) {
	eval := testLearningEvaluation()
	eval.Questions[1].Reason = "Retrying the smoke once isolates the flaky network step."
	eval.Questions[2].Reason = "Event 1 shows the failure and the passing rerun."
	candidate, ok := CandidateFromEvaluation(eval)
	if !ok {
		t.Fatal("candidate not created")
	}
	for _, want := range []string{
		"Reusable lesson extracted from a reviewed Beacon trace.",
		"Evaluator rationale:",
		"- reusable_correction: Retrying the smoke once isolates the flaky network step.",
		"- evidence_supported: Event 1 shows the failure and the passing rerun.",
		"- task_success: 0.95",
	} {
		if !strings.Contains(candidate.Body, want) {
			t.Fatalf("candidate body missing %q:\n%s", want, candidate.Body)
		}
	}
	if n := strings.Count(candidate.Body, "- task_success:"); n != 1 {
		t.Fatalf("task_success has no rationale and should appear once (signals only), got %d:\n%s", n, candidate.Body)
	}
	// The rationale reaches an installed skill through approval.
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	approved, memory, err := ApproveCandidate(store, candidate.ID, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	skill := RenderSkill(approved, memory)
	if !strings.Contains(skill, "Retrying the smoke once isolates the flaky network step.") {
		t.Fatalf("skill missing rationale:\n%s", skill)
	}
}

func TestCandidateBodyWithoutReasonsSaysNoLessonWasExtracted(t *testing.T) {
	eval := testLearningEvaluation()
	eval.Questions[0].Reason = "noul" // stored by earlier releases
	candidate, ok := CandidateFromEvaluation(eval)
	if !ok {
		t.Fatal("candidate not created")
	}
	if strings.Contains(candidate.Body, "Reusable lesson extracted") {
		t.Fatalf("body presents scores as an extracted lesson:\n%s", candidate.Body)
	}
	if strings.Contains(candidate.Body, "noul") || strings.Contains(candidate.Body, "Evaluator rationale:") {
		t.Fatalf("body renders a placeholder rationale:\n%s", candidate.Body)
	}
	for _, want := range []string{
		"no rationale",
		"Review the source trace before approving.",
		"Trace: trace-1",
		"- task_success: 0.95",
		"- reusable_correction: 0.90",
		"- evidence_supported: 0.85",
	} {
		if !strings.Contains(candidate.Body, want) {
			t.Fatalf("candidate body missing %q:\n%s", want, candidate.Body)
		}
	}
}
