package pisession

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectOncePrintsAndAdvancesCursor(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessions, "session-1.jsonl")
	writePiJSONL(t, sessionPath, []string{
		`{"type":"session","id":"session-1","cwd":"/work/project"}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"hello"}]}}`,
	})
	statePath := filepath.Join(dir, "state.json")
	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{
		SessionsDir: sessions,
		StatePath:   statePath,
		Print:       true,
		Out:         &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sessions != 1 || summary.SessionsChanged != 1 || summary.EventsEmitted != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if got := out.String(); !strings.Contains(got, `"action":"session.started"`) || !strings.Contains(got, `"action":"prompt.submitted"`) {
		t.Fatalf("output did not include expected events:\n%s", got)
	}

	out.Reset()
	summary, err = CollectOnce(CollectOptions{
		SessionsDir: sessions,
		StatePath:   statePath,
		Print:       true,
		Out:         &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 0 || summary.SessionsChanged != 0 {
		t.Fatalf("second summary = %#v", summary)
	}
	if out.Len() != 0 {
		t.Fatalf("second output = %q", out.String())
	}
}

func writePiJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
