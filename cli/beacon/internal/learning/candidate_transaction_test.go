package learning

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestReviewApprovalRollsBackAndRetries(t *testing.T) {
	for _, boundary := range []struct{ name, trigger string }{
		{"memory-insert", "BEFORE INSERT ON memories"},
		{"candidate-update", "BEFORE UPDATE ON candidates"},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			store := Open(filepath.Join(t.TempDir(), "memory.db"))
			candidate := putReviewCandidate(t, store, "approval")
			before := snapshotReview(t, store)
			setReviewFault(t, store, boundary.trigger)
			if _, _, err := ApproveCandidate(store, candidate.ID, "first review"); err == nil || !strings.Contains(err.Error(), "review write fault") {
				t.Fatalf("approval error = %v, want injected write failure", err)
			}
			if after := snapshotReview(t, store); !reflect.DeepEqual(after, before) {
				t.Errorf("failed approval changed persisted records or recall:\nbefore=%#v\nafter=%#v", before, after)
			}
			clearReviewFault(t, store)
			approved, memory, err := ApproveCandidateWithEdits(store, candidate.ID, "retry review", ApprovalEdits{
				Title: "Edited retry title", Body: " Reviewer-authored lesson. ", Kind: asymptoteobserve.LearningMemoryKindWorkflow,
			})
			if err != nil {
				t.Fatalf("retry with reviewer edits: %v", err)
			}
			if approved.State != asymptoteobserve.LearningCandidateStateApproved || approved.MemoryID != memory.ID || memory.Title != "Edited retry title" || memory.Body != "Reviewer-authored lesson." {
				t.Fatalf("retry result = %#v / %#v", approved, memory)
			}
			if got := snapshotReview(t, store); len(got.memories) != 1 || !reflect.DeepEqual(got.recalled, []string{memory.ID}) {
				t.Fatalf("retry left orphan or missing memory: %#v", got)
			}
		})
	}
}

func TestReviewSupersessionRollsBackAndRetries(t *testing.T) {
	for _, boundary := range []struct{ name, trigger string }{
		{"candidate-update", "BEFORE UPDATE ON candidates"},
		{"memory-update", "BEFORE UPDATE ON memories"},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			store := Open(filepath.Join(t.TempDir(), "memory.db"))
			candidate := putReviewCandidate(t, store, "original")
			_, original, err := ApproveCandidate(store, candidate.ID, "original review")
			if err != nil {
				t.Fatal(err)
			}
			replacementCandidate := putReviewCandidate(t, store, "replacement")
			_, replacement, err := ApproveCandidate(store, replacementCandidate.ID, "replacement review")
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotReview(t, store)
			setReviewFault(t, store, boundary.trigger)
			if _, err := SupersedeCandidate(store, candidate.ID, replacement.ID, "first supersession"); err == nil || !strings.Contains(err.Error(), "review write fault") {
				t.Fatalf("supersession error = %v, want injected write failure", err)
			}
			if after := snapshotReview(t, store); !reflect.DeepEqual(after, before) {
				t.Errorf("failed supersession changed persisted records or recall:\nbefore=%#v\nafter=%#v", before, after)
			}
			clearReviewFault(t, store)
			superseded, err := SupersedeCandidate(store, candidate.ID, replacement.ID, "retry supersession")
			if err != nil {
				t.Fatalf("retry supersession: %v", err)
			}
			stored, ok, err := store.GetMemory(original.ID)
			if err != nil || !ok || stored.SupersededBy != replacement.ID || superseded.SupersededBy != replacement.ID || superseded.State != asymptoteobserve.LearningCandidateStateSuperseded {
				t.Fatalf("retry records = %#v / %#v, found=%v err=%v", superseded, stored, ok, err)
			}
			if got := snapshotReview(t, store); !reflect.DeepEqual(got.recalled, []string{replacement.ID}) {
				t.Fatalf("superseded memory remains recallable: %#v", got)
			}
		})
	}
}

func TestReviewRejectRollsBackAndRetries(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate := putReviewCandidate(t, store, "reject")
	before := snapshotReview(t, store)
	setReviewFault(t, store, "BEFORE UPDATE ON candidates")
	if _, err := RejectCandidate(store, candidate.ID, "failed review"); err == nil || !strings.Contains(err.Error(), "review write fault") {
		t.Fatalf("reject error = %v, want injected write failure", err)
	}
	if after := snapshotReview(t, store); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed rejection changed records: before=%#v after=%#v", before, after)
	}
	clearReviewFault(t, store)
	rejected, err := RejectCandidate(store, candidate.ID, " retry rejection ")
	if err != nil || rejected.State != asymptoteobserve.LearningCandidateStateRejected || rejected.ReviewReason != "retry rejection" {
		t.Fatalf("retry rejection = %#v, err=%v", rejected, err)
	}
	if got := snapshotReview(t, store); len(got.memories) != 0 || len(got.recalled) != 0 {
		t.Fatalf("rejection created a memory: %#v", got)
	}
}

func TestReviewConcurrentApprovalAndRejectionStayConsistent(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	for iteration := 0; iteration < 12; iteration++ {
		candidate := putReviewCandidate(t, store, fmt.Sprintf("concurrent-%d", iteration))
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, _, err := ApproveCandidate(store, candidate.ID, "concurrent approval")
			results <- err
		}()
		go func() {
			<-start
			_, err := RejectCandidate(store, candidate.ID, "concurrent rejection")
			results <- err
		}()
		close(start)
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Errorf("iteration %d: want one review winner, errors = %v / %v", iteration, first, second)
		}
		stored, ok, err := store.GetCandidate(candidate.ID)
		if err != nil || !ok {
			t.Fatalf("read reviewed candidate: found=%v err=%v", ok, err)
		}
		memories, err := store.ListMemories(Query{ProjectID: candidate.Project.ID, Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		var linked []asymptoteobserve.LearningMemoryV1
		for _, memory := range memories {
			if memory.CandidateID == candidate.ID {
				linked = append(linked, memory)
			}
		}
		switch stored.State {
		case asymptoteobserve.LearningCandidateStateApproved:
			if len(linked) != 1 || linked[0].ID != stored.MemoryID {
				t.Errorf("iteration %d: approved candidate lacks its sole memory: %#v / %#v", iteration, stored, linked)
			}
		case asymptoteobserve.LearningCandidateStateRejected:
			if len(linked) != 0 || stored.MemoryID != "" {
				t.Errorf("iteration %d: rejected candidate has recallable memory: %#v / %#v", iteration, stored, linked)
			}
		default:
			t.Errorf("iteration %d: review state = %q", iteration, stored.State)
		}
	}
}

func putReviewCandidate(t *testing.T, store *Store, suffix string) asymptoteobserve.LearningCandidateV1 {
	t.Helper()
	candidate, ok := CandidateFromEvaluation(testLearningEvaluationWithID("eval-"+suffix, "trace-"+suffix))
	if !ok {
		t.Fatal("candidate not created")
	}
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func setReviewFault(t *testing.T, store *Store, boundary string) {
	t.Helper()
	execReviewSQL(t, store, "CREATE TRIGGER fail_review "+boundary+" BEGIN SELECT RAISE(ABORT, 'review write fault'); END")
}

func clearReviewFault(t *testing.T, store *Store) {
	t.Helper()
	execReviewSQL(t, store, "DROP TRIGGER fail_review")
}

func execReviewSQL(t *testing.T, store *Store, statement string) {
	t.Helper()
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

type reviewSnapshot struct {
	candidates []string
	memories   []string
	recalled   []string
}

func snapshotReview(t *testing.T, store *Store) reviewSnapshot {
	t.Helper()
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshot reviewSnapshot
	for _, table := range []struct {
		query string
		dest  *[]string
	}{
		{"SELECT candidate_json FROM candidates ORDER BY id", &snapshot.candidates},
		{"SELECT memory_json FROM memories ORDER BY id", &snapshot.memories},
	} {
		rows, err := db.Query(table.query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			*table.dest = append(*table.dest, value)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	memories, err := store.ListMemories(Query{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range memories {
		snapshot.recalled = append(snapshot.recalled, memory.ID)
	}
	return snapshot
}
