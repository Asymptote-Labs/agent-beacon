package learning

import (
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestCandidateFromEvaluationRequiresHighSignal(t *testing.T) {
	eval := testLearningEvaluation()
	eval.Score = 0.3
	if _, ok := CandidateFromEvaluation(eval); ok {
		t.Fatal("low-signal evaluation should not form a candidate")
	}
	eval.Score = 0.9
	candidate, ok := CandidateFromEvaluation(eval)
	if !ok {
		t.Fatal("high-signal evaluation did not form a candidate")
	}
	if candidate.Kind != asymptoteobserve.LearningMemoryKindDebuggingPattern {
		t.Fatalf("kind = %s", candidate.Kind)
	}
	if candidate.SourceEvaluationID != eval.ID || len(candidate.Evidence) != 1 || candidate.Evidence[0].TraceID != eval.Trace.ID {
		t.Fatalf("candidate evidence = %#v", candidate)
	}
}

func TestApproveRejectAndSupersedeCandidate(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, ok := CandidateFromEvaluation(testLearningEvaluation())
	if !ok {
		t.Fatal("candidate not created")
	}
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	approved, memory, err := ApproveCandidate(store, candidate.ID, "reviewed")
	if err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}
	if approved.State != asymptoteobserve.LearningCandidateStateApproved || approved.MemoryID != memory.ID {
		t.Fatalf("approved candidate = %#v memory=%#v", approved, memory)
	}
	if _, _, err := ApproveCandidate(store, candidate.ID, "again"); err == nil {
		t.Fatal("approving an approved candidate should fail")
	}

	rejectCandidate, ok := CandidateFromEvaluation(testLearningEvaluationWithID("eval-reject", "trace-reject"))
	if !ok {
		t.Fatal("reject candidate not created")
	}
	if err := store.PutCandidate(rejectCandidate); err != nil {
		t.Fatal(err)
	}
	rejected, err := RejectCandidate(store, rejectCandidate.ID, "not reusable")
	if err != nil {
		t.Fatalf("RejectCandidate: %v", err)
	}
	if rejected.State != asymptoteobserve.LearningCandidateStateRejected || rejected.ReviewReason != "not reusable" {
		t.Fatalf("rejected = %#v", rejected)
	}

	supersedeCandidate, ok := CandidateFromEvaluation(testLearningEvaluationWithID("eval-supersede", "trace-supersede"))
	if !ok {
		t.Fatal("supersede candidate not created")
	}
	if err := store.PutCandidate(supersedeCandidate); err != nil {
		t.Fatal(err)
	}
	superseded, err := SupersedeCandidate(store, supersedeCandidate.ID, memory.ID, "covered by approved memory")
	if err != nil {
		t.Fatalf("SupersedeCandidate: %v", err)
	}
	if superseded.State != asymptoteobserve.LearningCandidateStateSuperseded || superseded.SupersededBy != memory.ID {
		t.Fatalf("superseded = %#v", superseded)
	}
}

func testLearningEvaluation() asymptoteobserve.LearningEvaluationV1 {
	return testLearningEvaluationWithID("eval-1", "trace-1")
}

func testLearningEvaluationWithID(evalID, traceID string) asymptoteobserve.LearningEvaluationV1 {
	return asymptoteobserve.LearningEvaluationV1{
		SchemaVersion: asymptoteobserve.LearningSchemaVersion,
		ID:            evalID,
		Status:        asymptoteobserve.LearningEvaluationStatusCompleted,
		Score:         0.9,
		Project:       asymptoteobserve.LearningProjectV1{ID: "project-1", Path: "/repo"},
		Trace: asymptoteobserve.LearningTraceRefV1{
			ID:       traceID,
			Title:    "Fix flaky package smoke",
			Harness:  asymptoteobserve.TraceHarnessV1{Name: "cursor"},
			EventIDs: []string{"event-1", "event-2"},
		},
		Questions: []asymptoteobserve.LearningEvaluationQuestionV1{
			{ID: "task_success", Probability: 0.95},
			{ID: "reusable_correction", Probability: 0.90},
			{ID: "evidence_supported", Probability: 0.85},
		},
	}
}

// The evaluator scores traces but writes no lesson, so approval must carry the
// reviewer's text into the memory while leaving the candidate as it was scored.
func TestApproveCandidateWithEditsUsesReviewerText(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, ok := CandidateFromEvaluation(testLearningEvaluation())
	if !ok {
		t.Fatal("candidate not created")
	}
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApproveCandidateWithEdits(store, candidate.ID, "", ApprovalEdits{Kind: "lesson"}); err == nil {
		t.Fatal("an unknown kind should be refused")
	}
	approved, memory, err := ApproveCandidateWithEdits(store, candidate.ID, "reviewed", ApprovalEdits{
		Title:         "Run package smoke after build-pkg",
		Body:          "  Build the package before running smoke-endpoint.sh.\n",
		Applicability: "when editing packaging/macos",
		Kind:          asymptoteobserve.LearningMemoryKindWorkflow,
		Tags:          []string{"packaging", " ", "packaging", "macos"},
	})
	if err != nil {
		t.Fatalf("ApproveCandidateWithEdits: %v", err)
	}
	if memory.Title != "Run package smoke after build-pkg" || memory.Body != "Build the package before running smoke-endpoint.sh." {
		t.Fatalf("memory text = %q / %q", memory.Title, memory.Body)
	}
	if memory.Applicability != "when editing packaging/macos" || memory.Kind != asymptoteobserve.LearningMemoryKindWorkflow {
		t.Fatalf("memory = %#v", memory)
	}
	if len(memory.Tags) != 2 || memory.Tags[0] != "packaging" || memory.Tags[1] != "macos" {
		t.Fatalf("tags = %#v", memory.Tags)
	}
	if len(memory.Evidence) != 1 || memory.Evidence[0].TraceID != candidate.Evidence[0].TraceID {
		t.Fatalf("evidence = %#v", memory.Evidence)
	}
	stored, ok, err := store.GetCandidate(candidate.ID)
	if err != nil || !ok {
		t.Fatalf("GetCandidate: %v %v", ok, err)
	}
	if stored.Title != candidate.Title || stored.Body != candidate.Body || stored.MemoryID != approved.MemoryID {
		t.Fatalf("candidate should keep the evaluator's text: %#v", stored)
	}
}
