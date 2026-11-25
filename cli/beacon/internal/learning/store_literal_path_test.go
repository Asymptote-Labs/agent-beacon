package learning

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestStoreOpensLiteralURICharacters(t *testing.T) {
	for _, name := range []string{"project#draft", "project%25name"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name, "memory.db")
			store := Open(path)
			// A write creates the store; a read of a missing one does not (#1358).
			if err := store.PutEvaluation(asymptoteobserve.LearningEvaluationV1{ID: "eval-1", Project: asymptoteobserve.LearningProjectV1{ID: "project-1"}}); err != nil {
				t.Fatal(err)
			}
			got, err := store.Status()
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != path {
				t.Fatalf("path%q want%q", got.Path, path)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				t.Fatalf("literal database not populated: stat%v info%v", err, info)
			}
			if got.Evaluations != 1 {
				t.Fatalf("evaluations = %d, want 1", got.Evaluations)
			}
			if _, err = Open(path).Status(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
