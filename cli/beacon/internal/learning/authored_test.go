package learning

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func authoredTestTrace() asymptoteobserve.TraceShowResultV1 {
	return asymptoteobserve.TraceShowResultV1{
		Trace: asymptoteobserve.TraceSummaryV1{
			ID:      "session:claude_code:s1",
			Title:   "Fix the flaky smoke test",
			Harness: asymptoteobserve.TraceHarnessV1{Name: "claude_code"},
		},
		Events: []asymptoteobserve.TraceEventV1{{ID: "e1"}, {ID: "e2"}},
	}
}

func TestCandidateFromTraceRecordsEvidenceAndDefaults(t *testing.T) {
	project := asymptoteobserve.LearningProjectV1{ID: "project-1", Path: "/repo"}
	candidate, err := CandidateFromTrace(project, authoredTestTrace(), ApprovalEdits{
		Kind:  asymptoteobserve.LearningMemoryKindGotcha,
		Title: "  Smoke needs a warm cache  ",
		Body:  "Run smoke twice after a clean checkout.\n\n",
	})
	if err != nil {
		t.Fatalf("CandidateFromTrace: %v", err)
	}
	if !strings.HasPrefix(candidate.ID, "candidate_") {
		t.Fatalf("id = %q", candidate.ID)
	}
	if candidate.State != asymptoteobserve.LearningCandidateStateCandidate || candidate.SourceEvaluationID != "" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if candidate.Title != "Smoke needs a warm cache" || candidate.Body != "Run smoke twice after a clean checkout." {
		t.Fatalf("title=%q body=%q", candidate.Title, candidate.Body)
	}
	if candidate.Project.ID != "project-1" {
		t.Fatalf("project = %#v", candidate.Project)
	}
	if len(candidate.Evidence) != 1 || candidate.Evidence[0].TraceID != "session:claude_code:s1" ||
		strings.Join(candidate.Evidence[0].EventIDs, ",") != "e1,e2" || candidate.Evidence[0].Summary != "Fix the flaky smoke test" {
		t.Fatalf("evidence = %#v", candidate.Evidence)
	}
	if strings.Join(candidate.Tags, ",") != "beacon,gotcha,claude_code" {
		t.Fatalf("tags = %#v", candidate.Tags)
	}
	if candidate.Applicability == "" {
		t.Fatal("applicability should default when not provided")
	}
}

func TestCandidateFromTraceUsesReviewerTags(t *testing.T) {
	candidate, err := CandidateFromTrace(asymptoteobserve.LearningProjectV1{ID: "p"}, authoredTestTrace(), ApprovalEdits{
		Kind:  asymptoteobserve.LearningMemoryKindWorkflow,
		Title: "t",
		Body:  "b",
		Tags:  []string{"smoke", " smoke ", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(candidate.Tags, ",") != "smoke" {
		t.Fatalf("tags = %#v", candidate.Tags)
	}
}

func TestCandidateFromTraceValidates(t *testing.T) {
	project := asymptoteobserve.LearningProjectV1{ID: "project-1"}
	cases := map[string]ApprovalEdits{
		"missing kind":  {Title: "t", Body: "b"},
		"unknown kind":  {Kind: "anecdote", Title: "t", Body: "b"},
		"missing title": {Kind: asymptoteobserve.LearningMemoryKindWorkflow, Body: "b"},
		"missing body":  {Kind: asymptoteobserve.LearningMemoryKindWorkflow, Title: "t", Body: "  \n"},
	}
	for name, lesson := range cases {
		if _, err := CandidateFromTrace(project, authoredTestTrace(), lesson); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := CandidateFromTrace(project, asymptoteobserve.TraceShowResultV1{}, ApprovalEdits{Kind: "workflow", Title: "t", Body: "b"}); err == nil {
		t.Error("empty trace: expected an error")
	}
}

func TestAuthoredCandidateApprovesLikeAnyOther(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, err := CandidateFromTrace(asymptoteobserve.LearningProjectV1{ID: "p"}, authoredTestTrace(), ApprovalEdits{
		Kind: "gotcha", Title: "Smoke needs a warm cache", Body: "Run smoke twice.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	approved, memory, err := ApproveCandidate(store, candidate.ID, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != asymptoteobserve.LearningCandidateStateApproved || memory.Body != "Run smoke twice." || memory.Title != candidate.Title {
		t.Fatalf("approved=%#v memory=%#v", approved, memory)
	}
}

func TestProjectForTracePrefersExplicitPathThenRepository(t *testing.T) {
	repo := t.TempDir()
	trace := asymptoteobserve.TraceSummaryV1{Repository: &asymptoteobserve.TraceRepositoryV1{Path: repo}}
	fromTrace, err := ProjectForTrace("", trace)
	if err != nil {
		t.Fatal(err)
	}
	if fromTrace.Path != filepath.Clean(repo) {
		t.Fatalf("expected trace repository %q, got %#v", repo, fromTrace)
	}
	explicit := t.TempDir()
	fromFlag, err := ProjectForTrace(explicit, trace)
	if err != nil {
		t.Fatal(err)
	}
	if fromFlag.Path != filepath.Clean(explicit) {
		t.Fatalf("expected explicit path %q, got %#v", explicit, fromFlag)
	}
	cwd, err := ResolveProject("")
	if err != nil {
		t.Fatal(err)
	}
	fromCwd, err := ProjectForTrace("", asymptoteobserve.TraceSummaryV1{})
	if err != nil {
		t.Fatal(err)
	}
	if fromCwd.ID != cwd.ID {
		t.Fatalf("expected working-directory fallback %#v, got %#v", cwd, fromCwd)
	}
}
