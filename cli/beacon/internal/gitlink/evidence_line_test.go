package gitlink

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadLineSkipsOversizedLines(t *testing.T) {
	input := "short\n" + strings.Repeat("x", 300) + "\nafter\r\nlast-no-newline"
	r := bufio.NewReaderSize(strings.NewReader(input), 16)
	var got []string
	for {
		line, err := readLine(r, 100)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line))
	}
	want := []string{"short", "", "after", "last-no-newline"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReadEvidenceSurvivesAnOversizedLine(t *testing.T) {
	r := newTestRepo(t)
	repo := r.open()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	writeLog(t, logPath, logEvent{at: t0, harness: "codex", session: "s", cwd: repo.Root, action: "file.modified", filePath: filepath.Join(repo.Root, "x.go"), operation: "modify"})
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A corrupt line longer than any event, then a valid one after it.
	_, _ = f.WriteString(`{"session":"` + strings.Repeat("z", maxLineBytes+10) + "\n")
	f.Close()
	writeLogAppend(t, logPath, logEvent{at: t0, harness: "codex", session: "s2", cwd: repo.Root, action: "file.modified", filePath: filepath.Join(repo.Root, "y.go"), operation: "modify"})
	got, err := ReadEvidence(context.Background(), repo, logPath, t0.Add(-time.Hour), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got["codex_cli/s"] == nil || got["codex_cli/s2"] == nil {
		t.Fatalf("sessions %v", got)
	}
}
