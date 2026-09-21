package learning

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestPathForRuntimeLogUsesEndpointBaseDir(t *testing.T) {
	got := PathForRuntimeLog(filepath.Join("/tmp", "beacon", "logs", "runtime.jsonl"))
	want := filepath.Join("/tmp", "beacon", "memory.db")
	if got != want {
		t.Fatalf("PathForRuntimeLog = %s, want %s", got, want)
	}
}

func TestResolveProjectUsesGitRootAndOrigin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/repo.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	project, err := ResolveProject(child)
	if err != nil {
		t.Fatal(err)
	}
	if project.Path != root {
		t.Fatalf("project path = %s, want %s", project.Path, root)
	}
	if project.RemoteURL != "https://github.com/acme/repo.git" {
		t.Fatalf("remote = %q", project.RemoteURL)
	}
	if project.Branch != "feature" {
		t.Fatalf("branch = %q", project.Branch)
	}
	if project.ID == "" {
		t.Fatal("project ID is empty")
	}
}

func TestStorePersistsLearningArtifacts(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	project := asymptoteobserve.LearningProjectV1{ID: "project-1", Path: "/repo"}
	eval := asymptoteobserve.LearningEvaluationV1{
		ID:            "eval-1",
		Status:        asymptoteobserve.LearningEvaluationStatusCompleted,
		Project:       project,
		RubricVersion: "v1",
		RubricHash:    "sha256:rubric",
		Evaluator:     "jev",
		Trace:         asymptoteobserve.LearningTraceRefV1{ID: "trace-1", Title: "Fixed test flake"},
		Questions: []asymptoteobserve.LearningEvaluationQuestionV1{
			{ID: "task_success", Probability: 0.91, Confidence: 0.88},
		},
		Score: 0.91,
	}
	if err := store.PutEvaluation(eval); err != nil {
		t.Fatalf("PutEvaluation: %v", err)
	}
	gotEval, ok, err := store.GetEvaluation("eval-1")
	if err != nil || !ok {
		t.Fatalf("GetEvaluation ok=%v err=%v", ok, err)
	}
	if gotEval.Trace.ID != "trace-1" || gotEval.SchemaVersion != asymptoteobserve.LearningSchemaVersion {
		t.Fatalf("evaluation round trip = %#v", gotEval)
	}

	candidate := asymptoteobserve.LearningCandidateV1{
		ID:                 "candidate-1",
		State:              asymptoteobserve.LearningCandidateStateCandidate,
		Kind:               asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:              "Retry package smoke after flaky network",
		Body:               "When the package smoke fails on a transient fetch, rerun once after checking the error.",
		Project:            project,
		SourceEvaluationID: "eval-1",
		Evidence:           []asymptoteobserve.LearningEvidenceV1{{TraceID: "trace-1", EventIDs: []string{"event-1"}}},
	}
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatalf("PutCandidate: %v", err)
	}
	candidates, err := store.ListCandidates(Query{ProjectID: "project-1", State: asymptoteobserve.LearningCandidateStateCandidate})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != "candidate-1" {
		t.Fatalf("candidates = %#v", candidates)
	}

	memory := asymptoteobserve.LearningMemoryV1{
		ID:          "memory-1",
		CandidateID: "candidate-1",
		Kind:        candidate.Kind,
		Title:       candidate.Title,
		Body:        candidate.Body,
		Project:     project,
		Evidence:    candidate.Evidence,
	}
	if err := store.PutMemory(memory); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	memories, err := store.ListMemories(Query{ProjectID: "project-1", Q: "package smoke"})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 1 || memories[0].ID != "memory-1" {
		t.Fatalf("memories = %#v", memories)
	}
	status, err := store.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Evaluations != 1 || status.Candidates != 1 || status.ApprovedMemories != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestStoreScopesByProject(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	for _, projectID := range []string{"p1", "p2"} {
		if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
			ID:          "memory-" + projectID,
			CandidateID: "candidate-" + projectID,
			Kind:        asymptoteobserve.LearningMemoryKindConvention,
			Title:       "Project convention",
			Body:        "Use the local convention.",
			Project:     asymptoteobserve.LearningProjectV1{ID: projectID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	memories, err := store.ListMemories(Query{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].Project.ID != "p1" {
		t.Fatalf("project scoped memories = %#v", memories)
	}
}
