package cowork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintConfigIncludesEndpointAndPrivacyNote(t *testing.T) {
	out := PrintConfig(Config{Endpoint: "http://127.0.0.1:4318", Protocol: "HTTP/protobuf"})
	if !strings.Contains(out, "http://127.0.0.1:4318") {
		t.Fatalf("missing endpoint: %s", out)
	}
	if !strings.Contains(out, "prompt text") {
		t.Fatalf("missing privacy note: %s", out)
	}
}

func TestHasRecentCoworkEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := `{"harness":{"name":"claude_cowork"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if !HasRecentCoworkEvent(path) {
		t.Fatal("expected cowork event to be detected")
	}
}
