package factorysession

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func factoryMessage(id, role, ts, text string) string {
	return `{"type":"message","id":"` + id + `","timestamp":"` + ts + `","message":{"role":"` + role + `","content":[{"type":"text","text":"` + text + `"}]}}`
}

// A line longer than MaxLineBytes (a pasted image, a large tool result) is skipped and counted, and
// every record after it is collected, on the first sweep and on later ones as the session grows.
// bufio.Scanner stopped at it and reported the file as complete, so nothing after it was ever read.
func TestCollectOnceReadsPastAnOversizedLine(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "-repo", "session-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", MaxLineBytes+1024)
	writeFile(t, sessionPath, `{"type":"session_start","id":"session-1","title":"hi","cwd":"/repo"}`+"\n"+
		factoryMessage("u1", "user", "2026-05-16T22:11:04.611Z", "before the long line")+"\n"+
		`{"type":"message","id":"a1","timestamp":"2026-05-16T22:11:05.611Z","message":{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"t1","content":"`+huge+`"}]}}`+"\n"+
		factoryMessage("u2", "user", "2026-05-16T22:11:06.611Z", "after the long line")+"\n"+
		factoryMessage("a2", "assistant", "2026-05-16T22:11:07.611Z", "final answer")+"\n")

	statePath := filepath.Join(t.TempDir(), "factory-state.json")
	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if summary.MalformedLines != 1 || summary.PartialSessions != 0 {
		t.Errorf("summary = %+v, want the long line counted as one malformed line and the file read whole", summary)
	}
	for _, want := range []string{"before the long line", "after the long line", "final answer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("record %q was not collected", want)
		}
	}
	if strings.Contains(out.String(), huge[:64]) {
		t.Errorf("the oversized line's content was emitted")
	}

	// The session grows: the new record is read, and nothing already read is written again.
	appendFile(t, sessionPath, factoryMessage("u3", "user", "2026-05-16T22:12:00.000Z", "appended later")+"\n")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(sessionPath, later, later); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "appended later") {
		t.Errorf("a record appended after the long line was not collected")
	}
	if strings.Contains(out.String(), "final answer") {
		t.Errorf("a record already collected was written again")
	}
}

// A long line Factory is still writing is withheld like any unterminated tail, and once it is
// complete it is skipped and counted, keeping the numbers of the lines after it.
func TestReadWithholdsAnOversizedTailUntilItIsComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-1.jsonl")
	huge := strings.Repeat("x", MaxLineBytes+1024)
	writeFile(t, path, factoryMessage("u1", "user", "2026-05-16T22:11:04.611Z", "first")+"\n"+`{"type":"message","content":"`+huge)
	store := &Store{}
	records, stats, err := store.Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || stats.Lines != 1 || !stats.Partial || stats.Malformed != 0 {
		t.Fatalf("unterminated long tail: %d records, stats %+v; want 1 record, 1 line, partial", len(records), stats)
	}
	appendFile(t, path, `"}`+"\n"+factoryMessage("u2", "user", "2026-05-16T22:11:06.611Z", "third")+"\n")
	records, stats, err = store.Read(SessionRef{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || stats.Lines != 3 || stats.Partial || stats.Malformed != 1 {
		t.Fatalf("completed long line: %d records, stats %+v; want 2 records, 3 lines, 1 malformed", len(records), stats)
	}
	if records[1].Line != 3 {
		t.Errorf("record after the long line is line %d, want 3", records[1].Line)
	}
}

// A session an earlier release stopped at a long line has a cursor at the line before it that still
// records the file's full size and time, so an unchanged file was skipped before it was read. That
// cursor carries no long_lines mark: the session is read once more, the records after the long line
// are collected, and the ones the earlier release wrote are not written again (#1373 review).
func TestCollectOnceReadsPastALongLineAnEarlierReleaseStoppedAt(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "-repo", "session-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", MaxLineBytes+1024)
	writeFile(t, sessionPath, `{"type":"session_start","id":"session-1","title":"hi","cwd":"/repo"}`+"\n"+
		factoryMessage("u1", "user", "2026-05-16T22:11:04.611Z", "before the long line")+"\n"+
		`{"type":"message","id":"a1","timestamp":"2026-05-16T22:11:05.611Z","message":{"role":"assistant","content":[{"type":"text","text":"`+huge+`"}]}}`+"\n"+
		factoryMessage("u2", "user", "2026-05-16T22:11:06.611Z", "after the long line")+"\n")
	refs, err := (&Store{Dir: root}).List()
	if err != nil || len(refs) != 1 {
		t.Fatalf("List = %v, %v", refs, err)
	}
	// What the earlier reader saved: the two lines before the long one, and the whole file's size
	// and time.
	statePath := filepath.Join(t.TempDir(), "factory-state.json")
	state := &State{Version: StateVersion, Sessions: map[string]*Cursor{refs[0].ID: {
		SourcePath: refs[0].Path, LastLine: 2, Started: true,
		ModTimeUnixMS: refs[0].ModTimeUnixMS, SizeBytes: refs[0].SizeBytes, SettingsUnixMS: refs[0].SettingsUnixMS,
	}}}
	if err := state.Save(statePath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "after the long line") {
		t.Errorf("the record after the long line was not collected")
	}
	if strings.Contains(out.String(), "before the long line") || strings.Contains(out.String(), "session.started") {
		t.Errorf("records the earlier release already wrote were written again:\n%.400s", out.String())
	}
	// The cursor is now the new reader's, so an unchanged file is skipped again.
	out.Reset()
	if _, err := CollectOnce(CollectOptions{SessionsDir: root, StatePath: statePath, Print: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("a sweep of the unchanged file wrote:\n%.400s", out.String())
	}
}
