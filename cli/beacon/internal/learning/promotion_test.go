package learning

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// rubricEvaluation builds a completed evaluation whose Score is computed exactly
// as Evaluate computes it, so the tests exercise the real score and the gate
// together rather than a hand-picked Score.
func rubricEvaluation(taskSuccess, reusable, evidence float64) asymptoteobserve.LearningEvaluationV1 {
	eval := testLearningEvaluation()
	eval.Questions = normalizeQuestionResults([]asymptoteobserve.LearningEvaluationQuestionV1{
		{ID: TaskSuccessQuestionID, Probability: taskSuccess},
		{ID: "reusable_correction", Probability: reusable},
		{ID: "evidence_supported", Probability: evidence},
	})
	eval.Score = evaluationScore(eval.Questions)
	return eval
}

// Issue #649: the three real traces that were promoted although the judge said
// the task failed. Each clears CandidateScoreThreshold on the mean alone.
func TestPromotionRejectsFailedTasksThatClearTheMean(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		task, reusable, evidence float64
		wantScore                float64
		wantReasonContains       string
	}{
		{name: "task_success 0.27 promoted by +0.0067", task: 0.27, reusable: 0.86, evidence: 0.69, wantScore: 0.6067, wantReasonContains: "task_success 0.27 is below 0.50"},
		{name: "task_success 0.42 scored 0.6667", task: 0.42, reusable: 0.86, evidence: 0.72, wantScore: 0.6667, wantReasonContains: "task_success 0.42 is below 0.50"},
		{name: "task_success 0.42 scored 0.6367", task: 0.42, reusable: 0.80, evidence: 0.69, wantScore: 0.6367, wantReasonContains: "task_success 0.42 is below 0.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := rubricEvaluation(tc.task, tc.reusable, tc.evidence)
			if math.Abs(eval.Score-tc.wantScore) > 0.00005 {
				t.Fatalf("score = %.6f, want %.4f (the reported mean must not change)", eval.Score, tc.wantScore)
			}
			if eval.Score < CandidateScoreThreshold {
				t.Fatalf("fixture score %.4f no longer clears the score threshold, so it does not test the gate", eval.Score)
			}
			ok, reason := PromotionDecision(eval)
			if ok {
				t.Fatalf("PromotionDecision promoted a failed task (score %.4f)", eval.Score)
			}
			if !strings.Contains(reason, tc.wantReasonContains) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantReasonContains)
			}
			if candidate, ok := CandidateFromEvaluation(eval); ok {
				t.Fatalf("CandidateFromEvaluation promoted a failed task: %#v", candidate)
			}
		})
	}
}

func TestPromotionTaskSuccessGateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		task, reusable, evidence float64
		wantPromoted             bool
		wantReasonContains       string
	}{
		{name: "at gate, mean over threshold", task: CandidateTaskSuccessThreshold, reusable: 0.80, evidence: 0.80, wantPromoted: true},
		{name: "at gate, mean under threshold", task: CandidateTaskSuccessThreshold, reusable: 0.60, evidence: 0.60, wantReasonContains: "score 0.57 is below 0.60"},
		{name: "just below gate, mean well over threshold", task: 0.49, reusable: 0.95, evidence: 0.95, wantReasonContains: "task_success 0.49 is below 0.50"},
		{name: "fractionally below gate", task: 0.4999, reusable: 1.0, evidence: 1.0, wantReasonContains: "task_success 0.4999 is below 0.50"},
		{name: "below gate, mean under threshold", task: 0.10, reusable: 0.60, evidence: 0.60, wantReasonContains: "task_success 0.10 is below 0.50"},
		{name: "just above gate, mean over threshold", task: 0.51, reusable: 0.80, evidence: 0.80, wantPromoted: true},
		{name: "just above gate, mean under threshold", task: 0.51, reusable: 0.60, evidence: 0.60, wantReasonContains: "score 0.57 is below 0.60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := rubricEvaluation(tc.task, tc.reusable, tc.evidence)
			ok, reason := PromotionDecision(eval)
			if ok != tc.wantPromoted {
				t.Fatalf("PromotionDecision(%v/%v/%v, score %.4f) = %v (%q), want %v", tc.task, tc.reusable, tc.evidence, eval.Score, ok, reason, tc.wantPromoted)
			}
			if tc.wantPromoted && reason != "" {
				t.Fatalf("promoted evaluation carries a reason: %q", reason)
			}
			if !tc.wantPromoted && !strings.Contains(reason, tc.wantReasonContains) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantReasonContains)
			}
			if _, created := CandidateFromEvaluation(eval); created != tc.wantPromoted {
				t.Fatalf("CandidateFromEvaluation created=%v, want %v (must agree with PromotionDecision)", created, tc.wantPromoted)
			}
		})
	}
}

func TestPromotionKeepsSuccessfulTraceAndItsScore(t *testing.T) {
	eval := rubricEvaluation(0.9, 0.8, 0.7)
	if want := (0.9 + 0.8 + 0.7) / 3; math.Abs(eval.Score-want) > 1e-12 {
		t.Fatalf("score = %v, want the plain mean %v", eval.Score, want)
	}
	candidate, ok := CandidateFromEvaluation(eval)
	if !ok {
		t.Fatal("a successful, reusable trace was not promoted")
	}
	if candidate.SourceEvaluationID != eval.ID || candidate.State != asymptoteobserve.LearningCandidateStateCandidate {
		t.Fatalf("candidate = %#v", candidate)
	}
	if !strings.Contains(candidate.Body, "- task_success: 0.90") {
		t.Fatalf("candidate body lost the task_success signal:\n%s", candidate.Body)
	}
}

func TestPromotionRequiresTaskSuccessAnswer(t *testing.T) {
	eval := testLearningEvaluation()
	eval.Questions = []asymptoteobserve.LearningEvaluationQuestionV1{
		{ID: "reusable_correction", Probability: 0.95},
		{ID: "evidence_supported", Probability: 0.95},
	}
	eval.Score = 0.95
	ok, reason := PromotionDecision(eval)
	if ok || reason != "task_success was not answered" {
		t.Fatalf("PromotionDecision = %v, %q; want a refusal for a missing task_success answer", ok, reason)
	}
}

func TestPromotionRequiresCompletedStatus(t *testing.T) {
	for _, status := range []string{asymptoteobserve.LearningEvaluationStatusDryRun, asymptoteobserve.LearningEvaluationStatusFailed} {
		eval := rubricEvaluation(0.9, 0.9, 0.9)
		eval.Status = status
		ok, reason := PromotionDecision(eval)
		if ok || !strings.Contains(reason, status) {
			t.Fatalf("status %s: PromotionDecision = %v, %q", status, ok, reason)
		}
	}
}

// The gate sits on the whole Evaluate path: a real Jev answer for the #649 trace
// keeps its reported score and is not promoted, and an evaluator-supplied
// top-level score cannot bypass the task_success precondition.
func TestEvaluateThenPromoteAppliesTaskSuccessGate(t *testing.T) {
	eval := evaluateWithJevBody(t, `{"answers":{
		"task_success":{"type":"noul","noul":0.27},
		"reusable_correction":{"type":"noul","noul":0.86},
		"evidence_supported":{"type":"noul","noul":0.69}}}`)
	if got := fmt.Sprintf("%.4f", eval.Score); got != "0.6067" {
		t.Fatalf("stored score = %s, want the unchanged mean 0.6067", got)
	}
	if _, ok := CandidateFromEvaluation(eval); ok {
		t.Fatal("Jev-scored failed task was promoted")
	}

	legacy := evaluateWithJevBody(t, `{"score":0.95,"questions":[
		{"id":"task_success","probability":0.2},
		{"id":"reusable_correction","probability":0.95},
		{"id":"evidence_supported","probability":0.95}]}`)
	if legacy.Score != 0.95 {
		t.Fatalf("evaluator-supplied score = %v, want 0.95 kept as reported", legacy.Score)
	}
	if ok, reason := PromotionDecision(legacy); ok || !strings.Contains(reason, "task_success 0.20 is below 0.50") {
		t.Fatalf("evaluator-supplied score bypassed the gate: %v, %q", ok, reason)
	}

	passing := evaluateWithJevBody(t, `{"answers":{
		"task_success":{"type":"noul","noul":0.9},
		"reusable_correction":{"type":"noul","noul":0.8},
		"evidence_supported":{"type":"noul","noul":0.7}}}`)
	if _, ok := CandidateFromEvaluation(passing); !ok {
		t.Fatalf("successful Jev-scored trace was not promoted (score %.4f)", passing.Score)
	}
}
