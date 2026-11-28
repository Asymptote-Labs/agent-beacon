package primesession

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func primeMessage(role, text string) string {
	return `{"type":"message","message":{"role":"` + role + `","content":[{"type":"text","text":"` + text + `"}]}}`
}

func TestReadSkipsAnOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-1.jsonl")
	huge := strings.Repeat("x", maxLineBytes+1024)
	writePrimeJSONL(t, path, []string{
		`{"type":"session","id":"session-1","cwd":"/repo"}`,
		primeMessage("user", "before the long line"),
		primeMessage("user", huge),
		primeMessage("user", "after the long line"),
		primeMessage("assistant", "final answer"),
	})

	entries, stats, err := (&Store{}).Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MaxLine != 5 || stats.Malformed != 1 || stats.PartialTail {
		t.Fatalf("stats = %+v, want 5 lines, 1 malformed, no partial tail", stats)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	if entries[2].Line != 4 || entries[3].Line != 5 {
		t.Fatalf("later entry lines = %d, %d; want 4, 5", entries[2].Line, entries[3].Line)
	}
}

func TestReadWithholdsAnOversizedTailUntilItIsComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-1.jsonl")
	huge := strings.Repeat("x", maxLineBytes+1024)
	if err := os.WriteFile(path, []byte(primeMessage("user", "first")+"\n"+`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"`+huge), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &Store{}
	entries, stats, err := store.Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || stats.MaxLine != 1 || !stats.PartialTail || stats.Malformed != 0 {
		t.Fatalf("unterminated oversized tail: %d entries, stats %+v", len(entries), stats)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`"}]}}` + "\n" + primeMessage("user", "third") + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	entries, stats, err = store.Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || stats.MaxLine != 3 || stats.PartialTail || stats.Malformed != 1 {
		t.Fatalf("completed oversized line: %d entries, stats %+v", len(entries), stats)
	}
	if entries[1].Line != 3 {
		t.Fatalf("entry after oversized line is line %d, want 3", entries[1].Line)
	}

}

func TestReadWithholdsAShortPartialTail(t *testing.T) {
	store := &Store{}
	for _, tc := range []struct {
		name string
		tail string
	}{
		{name: "valid", tail: primeMessage("user", "still writing")},
		{name: "malformed", tail: `{"type":"message"`},
	} {
		t.Run("short_"+tc.name, func(t *testing.T) {
			shortPath := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(shortPath, []byte(primeMessage("user", "first")+"\n"+tc.tail), 0o644); err != nil {
				t.Fatal(err)
			}
			got, gotStats, err := store.Read(SessionRef{Path: shortPath})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || gotStats.MaxLine != 1 || !gotStats.PartialTail || gotStats.Malformed != 0 {
				t.Fatalf("%d entries, stats %+v; want one complete line and an uncounted partial tail", len(got), gotStats)
			}
		})
	}
}

func TestReadKeepsLineContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{\"type\":\"session\",\"id\":\"session-1\"}\r\n" +
		"\r\n" +
		"{\"type\":\"message\"\n" +
		"{\"type\":\"message\",\"value\":\"first\"}\r\n" +
		"{\"type\":\"message\",\"value\":\"last\"}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, stats, err := (&Store{}).Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []Entry{
		{Line: 1, Data: map[string]interface{}{"type": "session", "id": "session-1"}},
		{Line: 4, Data: map[string]interface{}{"type": "message", "value": "first"}},
		{Line: 5, Data: map[string]interface{}{"type": "message", "value": "last"}},
	}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("entries = %#v, want %#v", entries, wantEntries)
	}
	if stats != (ReadStats{MaxLine: 5, Malformed: 1}) {
		t.Fatalf("stats = %+v, want 5 lines, 1 malformed, no partial tail", stats)
	}
}

func TestReadLineReportsAnIOError(t *testing.T) {
	someErr := errors.New("read failed")
	_, status, err := readLine(bufio.NewReader(iotest.ErrReader(someErr)))
	if status != lineError {
		t.Fatalf("status = %v, want lineError", status)
	}
	if !errors.Is(err, someErr) {
		t.Fatalf("error = %v, want %v", err, someErr)
	}
}

func TestReadReturnsAnIOError(t *testing.T) {
	entries, stats, err := (&Store{}).Read(SessionRef{Path: t.TempDir()})
	if err == nil {
		t.Fatal("error = nil, want an I/O error reading a directory")
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}
	if stats != (ReadStats{}) {
		t.Fatalf("stats = %+v, want zero value", stats)
	}
}

func TestCollectOnceReadsPastAnOversizedLine(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessions, "session-1.jsonl")
	huge := "oversized-content-" + strings.Repeat("x", maxLineBytes+1024)
	writePrimeJSONL(t, sessionPath, []string{
		`{"type":"session","id":"session-1","cwd":"/repo"}`,
		primeMessage("user", "before the long line"),
		primeMessage("user", huge),
		primeMessage("user", "after the long line"),
		primeMessage("assistant", "final answer"),
	})

	statePath := filepath.Join(dir, "state.json")
	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{
		SessionsDir:  sessions,
		ArtifactsDir: filepath.Join(dir, "session-artifacts"),
		StatePath:    statePath,
		Print:        true,
		Out:          &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.MalformedLines != 1 {
		t.Fatalf("summary = %+v, want one malformed line", summary)
	}
	for _, want := range []string{"before the long line", "after the long line", "final answer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("record %q was not collected", want)
		}
	}
	if strings.Contains(out.String(), "oversized-content-") {
		t.Error("oversized line content was emitted")
	}

	file, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(primeMessage("user", "appended later") + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(sessionPath, later, later); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := CollectOnce(CollectOptions{
		SessionsDir:  sessions,
		ArtifactsDir: filepath.Join(dir, "session-artifacts"),
		StatePath:    statePath,
		Print:        true,
		Out:          &out,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "appended later") {
		t.Error("appended record was not collected")
	}
	for _, duplicate := range []string{"before the long line", "after the long line", "final answer"} {
		if strings.Contains(out.String(), duplicate) {
			t.Errorf("record %q was emitted twice", duplicate)
		}
	}
}
