package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sqliteuri"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMemoryRegenerationPreservesReviewedCandidates(t *testing.T) {
	for _, evaluated := range []bool{false, true} {
		for _, state := range []string{"candidate", "approved", "rejected", "superseded"} {
			name := "authored/" + state
			if evaluated {
				name = "evaluated/" + state
			}
			t.Run(name, func(t *testing.T) {
				configureMemoryRegeneration(t, evaluated)
				first := generateCommandCandidate(t, evaluated)
				store := memoryStore()
				var memoryID string
				if state == "approved" || state == "superseded" {
					_, memory, err := learning.ApproveCandidateWithEdits(store, first.ID, "reviewed original", learning.ApprovalEdits{
						Title: "Reviewer-approved title", Body: "Reviewer-approved lesson body", Kind: asymptoteobserve.LearningMemoryKindWorkflow,
					})
					if err != nil {
						t.Fatal(err)
					}
					memoryID = memory.ID
				}
				if state == "rejected" {
					if _, err := learning.RejectCandidate(store, first.ID, "not reusable"); err != nil {
						t.Fatal(err)
					}
				}
				if state == "superseded" {
					replacement := first
					replacement.Title = "Replacement lesson"
					replacement.ID = learning.CandidateID(replacement)
					if err := store.PutCandidate(replacement); err != nil {
						t.Fatal(err)
					}
					_, memory, err := learning.ApproveCandidate(store, replacement.ID, "replacement reviewed")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := learning.SupersedeCandidate(store, first.ID, memory.ID, "covered by replacement"); err != nil {
						t.Fatal(err)
					}
				}
				before := commandStoredCandidate(t, store, first.ID)
				beforeList, err := store.ListCandidates(learning.Query{})
				if err != nil {
					t.Fatal(err)
				}
				var beforeMemory asymptoteobserve.LearningMemoryV1
				if memoryID != "" {
					var ok bool
					beforeMemory, ok, err = store.GetMemory(memoryID)
					if err != nil || !ok {
						t.Fatalf("read reviewed memory: found=%v err=%v", ok, err)
					}
				}
				repeated := generateCommandCandidate(t, evaluated)
				after := commandStoredCandidate(t, store, first.ID)
				if repeated.ID != first.ID || after.State != state {
					t.Errorf("regeneration returned id=%s state=%s; stored state=%s, want same id and %s", repeated.ID, repeated.State, after.State, state)
				}
				if state != "candidate" {
					if !reflect.DeepEqual(after, before) {
						t.Errorf("regeneration changed review: state=%s memory=%q reason=%q approved=%q rejected=%q superseded=%q; want state=%s memory=%q reason=%q with unchanged record", after.State, after.MemoryID, after.ReviewReason, after.ApprovedAt, after.RejectedAt, after.SupersededAt, before.State, before.MemoryID, before.ReviewReason)
					}
					if !reflect.DeepEqual(repeated, before) {
						t.Errorf("generation response does not return persisted review: state=%s memory=%q reason=%q, want %s/%q/%q", repeated.State, repeated.MemoryID, repeated.ReviewReason, before.State, before.MemoryID, before.ReviewReason)
					}
				}
				afterList, err := store.ListCandidates(learning.Query{})
				if err != nil || len(afterList) != len(beforeList) {
					t.Errorf("regeneration changed candidate count: %d -> %d, err=%v", len(beforeList), len(afterList), err)
				}
				if memoryID != "" {
					afterMemory, ok, err := store.GetMemory(memoryID)
					if err != nil || !ok || !reflect.DeepEqual(afterMemory, beforeMemory) {
						t.Errorf("regeneration changed approved memory: found=%v err=%v", ok, err)
					}
				}
				if state == "approved" {
					var preview bytes.Buffer
					cmd := &cobra.Command{}
					cmd.SetOut(&preview)
					if err := runMemorySkillsPreview(cmd, []string{first.ID}); err != nil {
						t.Errorf("skill preview after regeneration: %v", err)
					} else if !strings.Contains(preview.String(), "Reviewer-approved lesson body") || !strings.Contains(preview.String(), memoryID) {
						t.Errorf("skill preview lost approved edits or association: %s", preview.String())
					}
				}
			})
		}
	}
}

func TestMemoryRegenerationDryRunLeavesReviewUntouched(t *testing.T) {
	_, _, calls := configureMemoryRegeneration(t, true)
	first := generateCommandCandidate(t, true)
	store := memoryStore()
	if _, _, err := learning.ApproveCandidate(store, first.ID, "reviewed before preview"); err != nil {
		t.Fatal(err)
	}
	before := commandStoredCandidate(t, store, first.ID)
	evaluations, err := store.ListEvaluations(learning.Query{})
	if err != nil {
		t.Fatal(err)
	}
	memoryOpts.dryRun = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryEvaluationsRun(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var result evaluationRunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || len(result.Previews) != 1 || result.EstimatedCalls != 1 || len(result.Candidates) != 0 || len(result.Evaluations) != 0 || calls.Load() != 1 {
		t.Fatalf("dry-run changed public preview/provider behavior: %#v, fake calls=%d", result, calls.Load())
	}
	if after := commandStoredCandidate(t, store, first.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("dry-run changed the reviewed candidate")
	}
	afterEvaluations, err := store.ListEvaluations(learning.Query{})
	if err != nil || !reflect.DeepEqual(afterEvaluations, evaluations) {
		t.Fatalf("dry-run changed evaluations: %v", err)
	}
}

func TestMemoryRegenerationTextShowsStoredState(t *testing.T) {
	for _, evaluated := range []bool{false, true} {
		t.Run(map[bool]string{false: "authored", true: "evaluated"}[evaluated], func(t *testing.T) {
			configureMemoryRegeneration(t, evaluated)
			first := generateCommandCandidate(t, evaluated)
			if _, _, err := learning.ApproveCandidate(memoryStore(), first.ID, "already reviewed"); err != nil {
				t.Fatal(err)
			}
			memoryOpts.jsonOutput = false
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			var err error
			if evaluated {
				err = runMemoryEvaluationsRun(cmd, nil)
			} else {
				err = runMemoryCandidatesCreate(cmd, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), first.ID+"\tapproved\t"+first.Kind) {
				t.Errorf("generation text does not show persisted approved state: %s", out.String())
			}
			if after := commandStoredCandidate(t, memoryStore(), first.ID); after.State != asymptoteobserve.LearningCandidateStateApproved {
				t.Errorf("text regeneration changed stored review to %s", after.State)
			}
		})
	}
}

func TestMemoryRegenerationChangedLessonAndEvidenceCreateCandidates(t *testing.T) {
	logPath, project, _ := configureMemoryRegeneration(t, false)
	first := generateCommandCandidate(t, false)
	store := memoryStore()
	if _, _, err := learning.ApproveCandidate(store, first.ID, "original reviewed"); err != nil {
		t.Fatal(err)
	}
	before := commandStoredCandidate(t, store, first.ID)
	memoryOpts.body = "A different lesson creates a different candidate."
	changedLesson := generateCommandCandidate(t, false)
	event := schema.Event{
		Timestamp: "2026-09-21T01:00:01Z", Vendor: schema.Vendor, Product: schema.Product, SchemaVersion: schema.SchemaVersion,
		Event:    schema.EventInfo{ID: "event-new", Kind: "agent_runtime", Action: "agent.message", Category: "agent"},
		Severity: schema.SeverityInfo, Endpoint: schema.EndpointInfo{OS: "darwin"}, Harness: schema.HarnessInfo{Name: "cursor"},
		Session: &schema.SessionInfo{ID: "s1", WorkingDirectory: project}, Message: "New trace evidence", Repository: project,
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append evidence: %v / %v", err, closeErr)
	}
	changedEvidence := generateCommandCandidate(t, false)
	if first.ID == changedLesson.ID || first.ID == changedEvidence.ID || changedLesson.ID == changedEvidence.ID {
		t.Fatalf("distinct lesson/evidence identities collapsed: %s / %s / %s", first.ID, changedLesson.ID, changedEvidence.ID)
	}
	if after := commandStoredCandidate(t, store, first.ID); !reflect.DeepEqual(after, before) {
		t.Fatal("changed lesson/evidence altered the original reviewed candidate")
	}
}

func TestMemoryGenerationWriteFailureDoesNotPrintCandidate(t *testing.T) {
	configureMemoryRegeneration(t, false)
	store := memoryStore()
	seed := testCommandCandidate(t)
	if err := store.PutCandidate(seed); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteuri.File(store.Path(), nil))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_generation BEFORE INSERT ON candidates BEGIN SELECT RAISE(ABORT, 'generation write fault'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListCandidates(learning.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runMemoryCandidatesCreate(cmd, nil); err == nil || !strings.Contains(err.Error(), "generation write fault") || out.Len() != 0 {
		t.Fatalf("failed generation returned err=%v output=%q", err, out.String())
	}
	after, err := store.ListCandidates(learning.Query{})
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("failed generation changed stored candidates: %v", err)
	}
}

func configureMemoryRegeneration(t *testing.T, evaluated bool) (string, string, *atomic.Int32) {
	t.Helper()
	logPath, project := writeMemoryCommandFixture(t)
	resetMemoryOpts(t)
	memoryOpts.logPath, memoryOpts.projectPath = logPath, project
	memoryOpts.traceID, memoryOpts.jsonOutput = "session:cursor:s1", true
	memoryOpts.kind, memoryOpts.title, memoryOpts.body = "workflow", "Original generated lesson", "Original generated lesson body"
	calls := new(atomic.Int32)
	if evaluated {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"questions":[{"id":"task_success","probability":0.9},{"id":"reusable_correction","probability":0.8},{"id":"evidence_supported","probability":0.7}]}`))
		}))
		t.Cleanup(server.Close)
		memoryOpts.jevEndpoint, memoryOpts.jevAPIKey, memoryOpts.jevModel = server.URL, "test-key", "jev-test"
	}
	return logPath, project, calls
}

func generateCommandCandidate(t *testing.T, evaluated bool) asymptoteobserve.LearningCandidateV1 {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if evaluated {
		if err := runMemoryEvaluationsRun(cmd, nil); err != nil {
			t.Fatal(err)
		}
		var result evaluationRunResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Candidates) != 1 {
			t.Fatalf("evaluation generation response: %v / %s", err, out.String())
		}
		return result.Candidates[0]
	}
	if err := runMemoryCandidatesCreate(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var candidate asymptoteobserve.LearningCandidateV1
	if err := json.Unmarshal(out.Bytes(), &candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func commandStoredCandidate(t *testing.T, store *learning.Store, id string) asymptoteobserve.LearningCandidateV1 {
	t.Helper()
	candidate, ok, err := store.GetCandidate(id)
	if err != nil || !ok {
		t.Fatalf("read candidate %s: found=%v err=%v", id, ok, err)
	}
	return candidate
}
