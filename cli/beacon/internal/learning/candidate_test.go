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
