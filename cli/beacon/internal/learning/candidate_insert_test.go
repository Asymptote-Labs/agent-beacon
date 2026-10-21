package learning

import (
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sqliteuri"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestInsertCandidateReturnsImmutableStoredRecord(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, _ := CandidateFromEvaluation(testLearningEvaluation())
	candidate.ID, candidate.SchemaVersion, candidate.State = "", "", ""
	first, err := store.InsertCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != CandidateID(candidate) || first.SchemaVersion != asymptoteobserve.LearningSchemaVersion || first.State != asymptoteobserve.LearningCandidateStateCandidate || first.CreatedAt == "" || first.UpdatedAt == "" {
		t.Fatalf("insert did not return normalized persisted record: %#v", first)
	}
	repeated, err := store.InsertCandidate(candidate)
	if err != nil || !reflect.DeepEqual(repeated, first) {
		t.Fatalf("pending replay changed stored record: %#v / %#v err=%v", first, repeated, err)
	}
	stored, ok, err := store.GetCandidate(first.ID)
	if err != nil || !ok || !reflect.DeepEqual(stored, first) {
		t.Fatalf("insert response differs from storage: %#v / %#v found=%v err=%v", first, stored, ok, err)
	}
	if _, _, err := ApproveCandidateWithEdits(store, first.ID, "intentional review", ApprovalEdits{Body: "Approved reviewer lesson"}); err != nil {
		t.Fatalf("explicit approval after generation: %v", err)
	}
	approved, ok, err := store.GetCandidate(first.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	repeated, err = store.InsertCandidate(candidate)
	if err != nil || !reflect.DeepEqual(repeated, approved) {
		t.Fatalf("replay did not return existing approval: %#v / %#v err=%v", repeated, approved, err)
	}
}

func TestInsertCandidateFaultsRollBackAndRetry(t *testing.T) {
	for _, fault := range []struct{ name, trigger string }{
		{"write", `BEFORE INSERT ON candidates BEGIN SELECT RAISE(ABORT, 'candidate insert fault'); END`},
		{"read", `AFTER INSERT ON candidates BEGIN UPDATE candidates SET candidate_json = '{' WHERE id = NEW.id; END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			store := Open(filepath.Join(t.TempDir(), "memory.db"))
			db, err := store.db()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TRIGGER fail_candidate_insert " + fault.trigger); err != nil {
				t.Fatal(err)
			}
			candidate, _ := CandidateFromEvaluation(testLearningEvaluation())
			if _, err := store.InsertCandidate(candidate); err == nil {
				t.Fatal("injected insertion/read failure returned success")
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM candidates`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed generation persisted %d rows, err=%v", count, err)
			}
			if _, ok, err := store.GetCandidate(candidate.ID); err != nil || ok {
				t.Fatalf("failed generation left an unreadable or visible candidate: found=%v err=%v", ok, err)
			}
			if _, err := db.Exec(`DROP TRIGGER fail_candidate_insert`); err != nil {
				t.Fatal(err)
			}
			retried, err := store.InsertCandidate(candidate)
			if err != nil || retried.ID != candidate.ID {
				t.Fatalf("retry after rolled back generation: %#v err=%v", retried, err)
			}
		})
	}
}

func TestInsertCandidateDoesNotRepairInvalidExistingRecord(t *testing.T) {
	for _, fault := range []struct{ name, raw string }{
		{"malformed", "{"},
		{"null", "null"},
		{"empty-object", "{}"},
		{"wrong-id", `{"id":"another_candidate","state":"approved"}`},
		{"array", "[]"},
		{"scalar", `"not a candidate"`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			store := Open(filepath.Join(t.TempDir(), "memory.db"))
			candidate, _ := CandidateFromEvaluation(testLearningEvaluation())
			if err := store.PutCandidate(candidate); err != nil {
				t.Fatal(err)
			}
			db, err := store.db()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE candidates SET candidate_json = ? WHERE id = ?`, fault.raw, candidate.ID); err != nil {
				t.Fatal(err)
			}
			if returned, err := store.InsertCandidate(candidate); err == nil {
				t.Errorf("invalid existing record returned success: %#v", returned)
			}
			var raw string
			if err := db.QueryRow(`SELECT candidate_json FROM candidates WHERE id = ?`, candidate.ID).Scan(&raw); err != nil || raw != fault.raw {
				t.Fatalf("generation overwrote existing invalid record: %q err=%v", raw, err)
			}
		})
	}
}

func TestInsertCandidateConcurrentCreationReturnsOneRecord(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	candidate, _ := CandidateFromEvaluation(testLearningEvaluation())
	type result struct {
		candidate asymptoteobserve.LearningCandidateV1
		err       error
	}
	const workers = 8
	start := make(chan struct{})
	results := make(chan result, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			value, err := store.InsertCandidate(candidate)
			results <- result{value, err}
		}()
	}
	close(start)
	var returned []asymptoteobserve.LearningCandidateV1
	for i := 0; i < workers; i++ {
		got := <-results
		if got.err != nil {
			t.Errorf("concurrent generation: %v", got.err)
		}
		returned = append(returned, got.candidate)
	}
	stored, ok, err := store.GetCandidate(candidate.ID)
	if err != nil || !ok {
		t.Fatalf("read generated record: found=%v err=%v", ok, err)
	}
	for _, got := range returned {
		if !reflect.DeepEqual(got, stored) {
			t.Errorf("concurrent insertion changed the canonical record: returned created=%q updated=%q, stored created=%q updated=%q", got.CreatedAt, got.UpdatedAt, stored.CreatedAt, stored.UpdatedAt)
		}
	}
	all, err := store.ListCandidates(Query{})
	if err != nil || len(all) != 1 {
		t.Fatalf("concurrent insert count = %d, err=%v", len(all), err)
	}
}

type candidateGenerationGate struct {
	opened atomic.Int32
	paused chan struct{}
	resume chan struct{}
}

var candidateGenerationGates sync.Map

func init() {
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		if value, ok := candidateGenerationGates.Load(dsn); ok {
			gate := value.(*candidateGenerationGate)
			if gate.opened.Add(1) == 1 {
				close(gate.paused)
				<-gate.resume
			}
		}
		return nil
	})
}

// Hold a pending generated value at its storage connection while a real review
// commits. An unconditional conflict update would erase that completed review.
func TestInsertCandidateDoesNotOverwriteConcurrentReview(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	candidate, _ := CandidateFromEvaluation(testLearningEvaluation())
	if err := store.PutCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	gate := &candidateGenerationGate{paused: make(chan struct{}), resume: make(chan struct{})}
	dsn := sqliteuri.File(store.Path(), url.Values{"_pragma": {"busy_timeout(5000)"}})
	candidateGenerationGates.Store(dsn, gate)
	var release sync.Once
	finished := make(chan struct{})
	var repeated asymptoteobserve.LearningCandidateV1
	var generateErr error
	t.Cleanup(func() {
		candidateGenerationGates.Delete(dsn)
		release.Do(func() { close(gate.resume) })
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("generation did not finish after releasing its connection")
		}
	})
	go func() {
		defer close(finished)
		repeated, generateErr = store.InsertCandidate(candidate)
	}()
	select {
	case <-gate.paused:
	case <-time.After(10 * time.Second):
		t.Fatal("generation did not reach its storage connection")
	}
	_, memory, err := ApproveCandidateWithEdits(store, candidate.ID, "reviewed during generation", ApprovalEdits{Body: "Preserve the reviewed lesson"})
	if err != nil {
		t.Fatal(err)
	}
	approved, ok, err := store.GetCandidate(candidate.ID)
	if err != nil || !ok {
		t.Fatalf("read committed review: found=%v err=%v", ok, err)
	}
	release.Do(func() { close(gate.resume) })
	<-finished
	if generateErr != nil || !reflect.DeepEqual(repeated, approved) {
		t.Errorf("generation did not return committed review: state=%s memory=%q reason=%q err=%v", repeated.State, repeated.MemoryID, repeated.ReviewReason, generateErr)
	}
	stored, ok, err := store.GetCandidate(candidate.ID)
	if err != nil || !ok || !reflect.DeepEqual(stored, approved) {
		t.Errorf("generation overwrote concurrent approval: state=%s memory=%q reason=%q found=%v err=%v", stored.State, stored.MemoryID, stored.ReviewReason, ok, err)
	}
	_, retained, err := CandidateMemoryForSkill(store, candidate.ID)
	if err != nil || retained.ID != memory.ID || !strings.Contains(RenderSkill(stored, retained), "Preserve the reviewed lesson") {
		t.Fatalf("generation broke approved skill access: memory=%s err=%v", retained.ID, err)
	}
}
