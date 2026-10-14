package claudesession

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReadHeadSkipsAnOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	huge := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", headMaxLineSize+10) + `"}}` + "\n"
	writeFile(t, path, huge+`{"type":"user","cwd":"/after/huge","message":{"role":"user","content":"second line"}}`+"\n")
	head := ReadHead(path)
	if head.CWD != "/after/huge" || head.FirstPrompt != "second line" {
		t.Fatalf("head = %+v; a line over the size cap must be skipped, not stop the read", head)
	}
}

func TestReadHeadStopsAfterTheLineBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	var b strings.Builder
	for range headMaxLines {
		b.WriteString(`{"type":"attachment"}` + "\n")
	}
	b.WriteString(`{"type":"user","cwd":"/too/late"}` + "\n")
	writeFile(t, path, b.String())
	if head := ReadHead(path); head.CWD != "" {
		t.Fatalf("head read past its %d-line budget: %+v", headMaxLines, head)
	}
}

func TestReadHeadToleratesMissingAndMalformedFiles(t *testing.T) {
	if head := ReadHead(filepath.Join(t.TempDir(), "missing.jsonl")); head != (Head{}) {
		t.Fatalf("missing file head = %+v", head)
	}
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	writeFile(t, path, "not json\n{\"type\":\"user\",\"cwd\":\"/ok\"}")
	if head := ReadHead(path); head.CWD != "/ok" {
		t.Fatalf("malformed line must be skipped and an unterminated last line read, got %+v", head)
	}
}
