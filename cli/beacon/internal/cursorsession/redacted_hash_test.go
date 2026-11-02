package cursorsession

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func storedTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// content.hash and file.diff_hash leave the machine where the text does not (Metadata-only keeps
// them), so each must be the hash of the text the writer stores after redaction: a hash of the
// text before it lets anyone holding the event confirm a guessed secret.
func TestMapTraceHashesWhatTheWriterStores(t *testing.T) {
	const secret = "deploy with ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8 and password=hunter2"
	ref := TraceRef{ID: "s-1", Kind: SourceTranscript, SourcePath: "/tmp/s-1.jsonl", Workspace: "/work"}
	records := []Record{
		{Order: 1, NativeID: "u1", Type: "user_message", Content: secret},
		{Order: 2, NativeID: "t1", Type: "tool_result", ToolName: "edit_file", Args: map[string]interface{}{"path": "main.go", "old_string": "x", "new_string": secret}},
		{Order: 3, NativeID: "u2", Type: "user_message", Content: "the quick brown fox"},
	}
	mapped := MapTrace(ref, records, MapOptions{})
	if len(mapped) != 3 {
		t.Fatalf("mapped = %d events, want 3: %+v", len(mapped), mapped)
	}

	prompt := asymptoteobserve.SanitizeEvent(mapped[0].Event, asymptoteobserve.DefaultMaxEventBytes)
	if prompt.Prompt.Text != "deploy with ghp_[REDACTED] and password=[REDACTED]" {
		t.Fatalf("stored prompt = %q", prompt.Prompt.Text)
	}
	if prompt.Content.Hash != storedTextHash(prompt.Prompt.Text) || prompt.Content.Bytes != len(secret) {
		t.Fatalf("prompt marker = %+v, want the hash of the stored text %q and the original size", prompt.Content, prompt.Prompt.Text)
	}

	originalDiffBytes := len(mapped[1].Event.File.Diff) // SanitizeEvent rewrites it through the pointer
	edit := asymptoteobserve.SanitizeEvent(mapped[1].Event, asymptoteobserve.DefaultMaxEventBytes)
	if edit.File == nil || !strings.Contains(edit.File.Diff, "+deploy with ghp_[REDACTED] and password=[REDACTED]") {
		t.Fatalf("edit file = %+v", edit.File)
	}
	if edit.File.DiffHash != storedTextHash(edit.File.Diff) || edit.File.DiffBytes != originalDiffBytes || originalDiffBytes == len(edit.File.Diff) {
		t.Fatalf("diff_hash %s over %d bytes, want the hash of the stored diff %q and the original size", edit.File.DiffHash, edit.File.DiffBytes, edit.File.Diff)
	}
	if edit.Content.Hash != edit.File.DiffHash || !edit.Content.Redacted {
		t.Fatalf("edit marker = %+v, want the diff_hash and redacted", edit.Content)
	}

	// Text with nothing to redact keeps the hash it always had.
	if plain := mapped[2].Event.Content; plain.Hash != "9ecb36561341d18eb65484e833efea61edc74b84cf5e6ae1b81c63533e25fc8f" || plain.Bytes != 19 {
		t.Fatalf("plain marker = %+v", plain)
	}
}
