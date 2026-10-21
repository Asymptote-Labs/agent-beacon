package learning

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestLearningSearchPaginatesMatchingRows(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	rows := []struct {
		id, title, project, candidateState, evaluationState, kind string
	}{
		{"foreign", "Needle recovery", "other-project", "candidate", "completed", "workflow"},
		{"wrong-state", "Needle recovery", "project", "rejected", "failed", "workflow"},
		{"wrong-kind", "Needle recovery", "project", "candidate", "failed", "convention"},
		{"nonmatch-a", "Ordinary task", "project", "candidate", "completed", "workflow"},
		{"match-a", "Needle recovery", "project", "candidate", "completed", "workflow"},
		{"nonmatch-b", "Needle without the other term", "project", "candidate", "completed", "workflow"},
		{"match-b", "Older needle recovery", "project", "candidate", "completed", "workflow"},
	}
	for i, row := range rows {
		putLearningSearchRow(t, store, row.id, row.title, row.project, row.candidateState, row.evaluationState, row.kind, len(rows)-i)
	}

	for _, collection := range learningSearchCollections(store) {
		t.Run(collection.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, query string
				limit, page int
				want        []string
			}{
				{"first match page", "NEEDLE recovery", 1, 1, []string{"match-a"}},
				{"second match page", "NEEDLE recovery", 1, 2, []string{"match-b"}},
				{"beyond last match page", "needle recovery", 1, 3, nil},
				{"both matches", "needle recovery", 2, 1, []string{"match-a", "match-b"}},
				{"default limit", "needle recovery", 0, 1, []string{"match-a", "match-b"}},
				{"negative page", "needle recovery", 1, -1, []string{"match-a"}},
				{"zero page", "needle recovery", 1, 0, []string{"match-a"}},
				{"no matches", "absent", 1, 1, nil},
				{"empty query", "", 1, 2, []string{"match-a"}},
				{"whitespace query", " \t\n ", 1, 2, []string{"match-a"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					query := collection.query
					query.Q, query.Limit, query.Page = tc.query, tc.limit, tc.page
					got, err := collection.list(query)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Equal(got, tc.want) {
						t.Fatalf("IDs = %v, want %v", got, tc.want)
					}
				})
			}
		})
	}
}

func TestLearningSearchFindsMatchesBeyondDefaultPage(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	putLearningSearchRow(t, store, "match", "Needle recovery", "project", "candidate", "completed", "workflow", 0)
	for i := 1; i <= 51; i++ {
		putLearningSearchRow(t, store, fmt.Sprintf("unrelated-%d", i), "Ordinary task", "project", "candidate", "completed", "workflow", i)
	}
	for _, collection := range learningSearchCollections(store) {
		t.Run(collection.name, func(t *testing.T) {
			query := collection.query
			query.Q = "needle"
			got, err := collection.list(query)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{"match"}) {
				t.Fatalf("IDs = %v, want the match beyond the default raw page", got)
			}
		})
	}
}

func TestLearningSearchLimitBounds(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	for _, collection := range learningSearchCollections(store) {
		if got, err := collection.list(collection.query); err != nil || len(got) != 0 {
			t.Fatalf("empty %s: IDs = %v, error = %v", collection.name, got, err)
		}
	}
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 502; i++ {
		id := fmt.Sprintf("match-%03d", i)
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		candidate := asymptoteobserve.LearningCandidateV1{
			ID: id, Title: "Needle recovery", Project: asymptoteobserve.LearningProjectV1{ID: "project"}, State: "candidate", Kind: "workflow",
		}
		evaluation := asymptoteobserve.LearningEvaluationV1{
			ID: id, Project: candidate.Project, Status: "completed", Trace: asymptoteobserve.LearningTraceRefV1{ID: id, Title: candidate.Title},
		}
		candidateJSON, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		evaluationJSON, err := json.Marshal(evaluation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO candidates (id, project_id, state, kind, title, created_at, updated_at, candidate_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "project", candidate.State, candidate.Kind, candidate.Title, at, at, string(candidateJSON)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO evaluations (id, project_id, trace_id, status, created_at, updated_at, rubric_hash, evaluator, evaluation_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "project", id, evaluation.Status, at, at, "rubric", "jev", string(evaluationJSON)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, collection := range learningSearchCollections(store) {
		t.Run(collection.name, func(t *testing.T) {
			for _, tc := range []struct {
				limit, page, count int
				first              string
			}{
				{0, 1, 50, "match-501"},
				{-1, 1, 50, "match-501"},
				{500, 1, 500, "match-501"},
				{1000, 2, 2, "match-001"},
			} {
				query := collection.query
				query.Q, query.Limit, query.Page = "needle", tc.limit, tc.page
				got, err := collection.list(query)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != tc.count || got[0] != tc.first {
					t.Fatalf("limit %d, page %d: IDs = %v, want %d starting with %s", tc.limit, tc.page, got, tc.count, tc.first)
				}
			}
		})
	}
}

func TestLearningSearchStopsAfterFullPage(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "memory.db"))
	putLearningSearchRow(t, store, "older", "Ordinary task", "project", "candidate", "completed", "workflow", 0)
	putLearningSearchRow(t, store, "match", "Needle recovery", "project", "candidate", "completed", "workflow", 1)
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE candidates SET candidate_json = 'invalid' WHERE id = 'older'; UPDATE evaluations SET evaluation_json = 'invalid' WHERE id = 'older'`); err != nil {
		t.Fatal(err)
	}
	for _, collection := range learningSearchCollections(store) {
		t.Run(collection.name, func(t *testing.T) {
			query := collection.query
			query.Q, query.Limit = "needle", 1
			got, err := collection.list(query)
			if err != nil {
				t.Fatalf("read beyond the full matching page: %v", err)
			}
			if !slices.Equal(got, []string{"match"}) {
				t.Fatalf("IDs = %v, want [match]", got)
			}
		})
	}
}

func putLearningSearchRow(t *testing.T, store *Store, id, title, project, candidateState, evaluationState, kind string, order int) {
	t.Helper()
	if err := store.PutCandidate(asymptoteobserve.LearningCandidateV1{
		ID: id, Title: title, Project: asymptoteobserve.LearningProjectV1{ID: project}, State: candidateState, Kind: kind,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutEvaluation(asymptoteobserve.LearningEvaluationV1{
		ID: id, Project: asymptoteobserve.LearningProjectV1{ID: project}, Status: evaluationState,
		Trace: asymptoteobserve.LearningTraceRefV1{ID: id, Title: title}, RubricHash: "rubric",
	}); err != nil {
		t.Fatal(err)
	}
	db, err := store.db()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(order) * time.Second).Format(time.RFC3339)
	for _, stmt := range []string{`UPDATE candidates SET updated_at = ? WHERE id = ?`, `UPDATE evaluations SET updated_at = ? WHERE id = ?`} {
		if _, err := db.Exec(stmt, at, id); err != nil {
			t.Fatal(err)
		}
	}
}

func learningSearchCollections(store *Store) []struct {
	name  string
	query Query
	list  func(Query) ([]string, error)
} {
	return []struct {
		name  string
		query Query
		list  func(Query) ([]string, error)
	}{
		{"candidates", Query{ProjectID: "project", State: "candidate", Kind: "workflow"}, func(query Query) ([]string, error) {
			values, err := store.ListCandidates(query)
			var ids []string
			for _, value := range values {
				ids = append(ids, value.ID)
			}
			return ids, err
		}},
		{"evaluations", Query{ProjectID: "project", State: "completed"}, func(query Query) ([]string, error) {
			values, err := store.ListEvaluations(query)
			var ids []string
			for _, value := range values {
				ids = append(ids, value.ID)
			}
			return ids, err
		}},
	}
}
