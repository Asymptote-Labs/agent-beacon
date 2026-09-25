package learning

import (
	"fmt"
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

// A linked worktree's .git is a file pointing at .git/worktrees/<name> in the main
// checkout, whose commondir points back at the shared .git. The worktree must resolve
// to the same project ID as the main checkout, with its own branch.
func TestResolveProjectFollowsLinkedWorktree(t *testing.T) {
	main := t.TempDir()
	mainGit := filepath.Join(main, ".git")
	worktreeGit := filepath.Join(mainGit, "worktrees", "check-brain")
	if err := os.MkdirAll(worktreeGit, 0755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(mainGit, "HEAD"):          "ref: refs/heads/main\n",
		filepath.Join(mainGit, "config"):        "[remote \"origin\"]\n\turl = https://github.com/acme/repo.git\n",
		filepath.Join(worktreeGit, "HEAD"):      "ref: refs/heads/lawrence/check-brain\n",
		filepath.Join(worktreeGit, "commondir"): "../..\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	worktree := filepath.Join(t.TempDir(), "check-brain")
	if err := os.MkdirAll(filepath.Join(worktree, "server"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+worktreeGit+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	fromMain, err := ResolveProject(main)
	if err != nil {
		t.Fatal(err)
	}
	fromWorktree, err := ResolveProject(filepath.Join(worktree, "server"))
	if err != nil {
		t.Fatal(err)
	}
	if fromWorktree.Path != worktree {
		t.Fatalf("worktree path = %s, want %s", fromWorktree.Path, worktree)
	}
	if fromWorktree.RemoteURL != "https://github.com/acme/repo.git" {
		t.Fatalf("worktree remote = %q", fromWorktree.RemoteURL)
	}
	if fromWorktree.Branch != "lawrence/check-brain" {
		t.Fatalf("worktree branch = %q", fromWorktree.Branch)
	}
	if fromWorktree.ID != fromMain.ID {
		t.Fatalf("worktree project %s should match main checkout %s", fromWorktree.ID, fromMain.ID)
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

func TestListMemoriesSearchesAllRows(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	project := asymptoteobserve.LearningProjectV1{ID: "proj"}
	for i := 0; i < 10; i++ {
		if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
			ID:          fmt.Sprintf("memory-%d", i),
			CandidateID: fmt.Sprintf("candidate-%d", i),
			Kind:        asymptoteobserve.LearningMemoryKindConvention,
			Title:       fmt.Sprintf("Convention %d", i),
			Body:        "Generic body text.",
			Project:     project,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
		ID:          "memory-target",
		CandidateID: "candidate-target",
		Kind:        asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:       "Unique needle title",
		Body:        "Special body for matching.",
		Project:     project,
		CreatedAt:   "2020-01-01T00:00:00Z",
		UpdatedAt:   "2020-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	memories, err := store.ListMemories(Query{ProjectID: "proj", Q: "needle", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].ID != "memory-target" {
		t.Fatalf("expected to find older matching memory, got %d results", len(memories))
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

func TestListMemoriesAppliesTextMatchBeforeLimit(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	project := asymptoteobserve.LearningProjectV1{ID: "project-1"}
	if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
		ID:          "memory-older-match",
		CandidateID: "candidate-older-match",
		Kind:        asymptoteobserve.LearningMemoryKindDebuggingPattern,
		Title:       "Older matching memory",
		Body:        "Remember the rare unique-needle recovery step.",
		Project:     project,
	}); err != nil {
		t.Fatal(err)
	}
	setMemoryUpdatedAt(t, store, "memory-older-match", "2026-01-01T00:00:00Z")
	for i := 0; i < 5; i++ {
		id := "memory-newer-" + string(rune('a'+i))
		if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
			ID:          id,
			CandidateID: "candidate-" + id,
			Kind:        asymptoteobserve.LearningMemoryKindConvention,
			Title:       "Newer unrelated memory",
			Body:        "Use the ordinary local workflow.",
			Project:     project,
		}); err != nil {
			t.Fatal(err)
		}
		setMemoryUpdatedAt(t, store, id, "2026-01-02T00:00:0"+string(rune('0'+i))+"Z")
	}
	memories, err := store.ListMemories(Query{ProjectID: "project-1", Q: "unique-needle", Limit: 5})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 1 || memories[0].ID != "memory-older-match" {
		t.Fatalf("memories = %#v", memories)
	}
}

func setMemoryUpdatedAt(t *testing.T, store *Store, id, updatedAt string) {
	t.Helper()
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE memories SET updated_at = ? WHERE id = ?`, updatedAt, id); err != nil {
		t.Fatal(err)
	}
}

func TestProjectIDForPathMatchesTheMostSpecificKnownProject(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	// Absolute on every platform, because stored project paths are: ProjectIDForPath makes the
	// relayed path absolute before comparing, and on Windows "/work" gains a drive letter that a
	// stored "/work" never has. Nothing here is created on disk; resolution must not touch it.
	root := t.TempDir()
	outer := asymptoteobserve.LearningProjectV1{ID: "project-outer", Path: filepath.Join(root, "work")}
	inner := asymptoteobserve.LearningProjectV1{ID: "project-inner", Path: filepath.Join(root, "work", "repo")}
	for _, project := range []asymptoteobserve.LearningProjectV1{outer, inner} {
		memory := asymptoteobserve.LearningMemoryV1{
			ID:          "memory-" + project.ID,
			CandidateID: "candidate-" + project.ID,
			Kind:        asymptoteobserve.LearningMemoryKindConvention,
			Title:       "Title for " + project.ID,
			Project:     project,
		}
		if err := store.PutMemory(memory); err != nil {
			t.Fatalf("PutMemory: %v", err)
		}
	}

	got, err := store.ProjectIDForPath(filepath.Join(root, "work", "repo", "cli", "beacon"))
	if err != nil {
		t.Fatalf("ProjectIDForPath: %v", err)
	}
	if got != inner.ID {
		t.Fatalf("project ID = %q, want %q", got, inner.ID)
	}

	elsewhere := filepath.Join(root, "elsewhere")
	unknown, err := store.ProjectIDForPath(elsewhere)
	if err != nil {
		t.Fatalf("ProjectIDForPath: %v", err)
	}
	if want := ProjectID(asymptoteobserve.LearningProjectV1{Path: elsewhere}); unknown != want {
		t.Fatalf("unknown project ID = %q, want the path-derived ID %q", unknown, want)
	}
}

func TestListMemoriesScopesARelayedProjectPathToOneProject(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	root := t.TempDir() // absolute on every platform; see TestProjectIDForPathMatchesTheMostSpecificKnownProject
	for _, project := range []asymptoteobserve.LearningProjectV1{
		{ID: "project-a", Path: filepath.Join(root, "work", "a")},
		{ID: "project-b", Path: filepath.Join(root, "work", "b")},
	} {
		memory := asymptoteobserve.LearningMemoryV1{
			ID:          "memory-" + project.ID,
			CandidateID: "candidate-" + project.ID,
			Kind:        asymptoteobserve.LearningMemoryKindConvention,
			Title:       "Title for " + project.ID,
			Project:     project,
		}
		if err := store.PutMemory(memory); err != nil {
			t.Fatalf("PutMemory: %v", err)
		}
	}

	memories, err := store.ListMemories(Query{ProjectPath: filepath.Join(root, "work", "a", "sub")})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 1 || memories[0].Project.ID != "project-a" {
		t.Fatalf("memories = %#v, want only project-a", memories)
	}

	none, err := store.ListMemories(Query{ProjectPath: filepath.Join(root, "work", "c")})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown project path returned %d memories, want none", len(none))
	}
}

// A relayed ProjectPath arrives in a request, so resolving it must not read
// the filesystem. The store holds the project under the ID its origin URL
// produces; if the query still resolved the path through .git, it would find
// that same ID and return the row.
func TestRelayedProjectPathDoesNotReadGitMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/repo.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveProject(root)
	if err != nil {
		t.Fatal(err)
	}
	project := asymptoteobserve.LearningProjectV1{ID: resolved.ID, RemoteURL: resolved.RemoteURL}

	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err := store.PutMemory(asymptoteobserve.LearningMemoryV1{
		ID:          "memory-1",
		CandidateID: "candidate-1",
		Kind:        asymptoteobserve.LearningMemoryKindConvention,
		Title:       "Recorded against the origin URL",
		Project:     project,
	}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	memories, err := store.ListMemories(Query{ProjectPath: root})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("relayed project path resolved through git metadata: %#v", memories)
	}

	byID, err := store.ListMemories(Query{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(byID) != 1 {
		t.Fatalf("memories by project ID = %d, want 1", len(byID))
	}
}
