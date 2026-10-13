package learning

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreOpensLiteralURICharacters(t *testing.T) {
	for _, name := range []string{"plain", "project#draft", "project%25name"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name, "memory.db")
			store := Open(path)
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
			if _, err = Open(path).Status(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
