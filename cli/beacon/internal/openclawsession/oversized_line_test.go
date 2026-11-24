package openclawsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A session line longer than maxLineBytes (an inline image, a large tool result) is skipped like a
// line that is not JSON, and the session and its other records are collected. bufio.Scanner failed
// the whole file there, which dropped the session from the listing without an error.
func TestCollectOnceReadsASessionWithAnOversizedLine(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, ".openclaw", "agents", "main", "sessions")
	huge := strings.Repeat("x", maxLineBytes+1024)
	lines := append(openClawLines("s1", "/repo"),
		`{"type":"message","id":"u2","timestamp":"2026-09-19T23:00:03Z","message":{"role":"user","content":[{"type":"text","text":"`+huge+`"}]}}`,
		`{"type":"message","id":"a2","timestamp":"2026-09-19T23:00:04Z","message":{"role":"assistant","modelId":"openai/gpt-5","content":[{"type":"text","text":"after the long line"}]}}`,
	)
	writeFile(t, filepath.Join(sessions, "s1.jsonl"), lines...)
	logPath := filepath.Join(dir, "runtime.jsonl")
	summary, err := CollectOnce(CollectOptions{OpenClawDir: filepath.Join(dir, ".openclaw"), StatePath: filepath.Join(dir, "state.json"), Write: true, LogPath: logPath, UserMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Traces != 1 {
		t.Fatalf("Traces = %d, want 1", summary.Traces)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"summarize the repo", "echo hi", "after the long line"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record %q was not collected", want)
		}
	}
	if strings.Contains(string(data), huge[:64]) {
		t.Errorf("the oversized line's content was written")
	}
}

// The last line is read with or without its newline, as bufio.Scanner read it.
func TestReadEntriesKeepsAnUnterminatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(openClawLines("s1", "/repo"), "\r\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := readEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[2].ID != "a1" {
		t.Fatalf("readEntries = %d entries, want all 3 with the unterminated last one", len(entries))
	}
}
