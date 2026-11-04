package cowork

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetStatusIncludesLastEventObservedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := `{"timestamp":"2026-05-13T16:20:40Z","harness":{"name":"claude_cowork"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	status := GetStatus(path)
	if !status.LastEventObserved {
		t.Fatal("expected event to be observed")
	}
	if got, want := status.LastEventObservedAt, "2026-05-13T16:20:40Z"; got != want {
		t.Fatalf("LastEventObservedAt = %q, want %q", got, want)
	}
}
