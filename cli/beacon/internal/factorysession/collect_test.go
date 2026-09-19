package factorysession

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectOnceReadsFactorySessionsIncrementally(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(project, "session-1.jsonl")
	writeFile(t, sessionPath,
		`{"type":"session_start","id":"session-1","title":"hi","cwd":"/repo"}`+"\n"+
			`{"type":"message","id":"u1","timestamp":"2026-05-16T22:11:04.611Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n")
	writeFile(t, filepath.Join(project, "session-1.settings.json"), `{"model":"claude-opus-4-7","tokenUsage":{"inputTokens":1,"outputTokens":2}}`)

	statePath := filepath.Join(t.TempDir(), "factory-state.json")
	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sessions != 1 || summary.EventsEmitted != 3 {
		t.Fatalf("summary = %+v, want one session and three events", summary)
	}
	text := out.String()
	for _, want := range []string{`"session.started"`, `"prompt.submitted"`, `"token.usage"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %s:\n%s", want, text)
		}
	}

	out.Reset()
	summary, err = CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 0 || out.Len() != 0 {
		t.Fatalf("second sweep emitted summary=%+v output=%s, want no events", summary, out.String())
	}

	appendFile(t, sessionPath, `{"type":"message","id":"a1","timestamp":"2026-05-16T22:11:05.611Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n")
	now := time.Now()
	if err := os.Chtimes(sessionPath, now, now); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	summary, err = CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if summary.EventsEmitted != 1 || !strings.Contains(out.String(), `"agent.message"`) {
		t.Fatalf("third sweep summary=%+v output=%s, want new assistant event", summary, out.String())
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
