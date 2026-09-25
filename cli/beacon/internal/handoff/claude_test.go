package handoff

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
)

func claudeLine(t *testing.T, entry map[string]interface{}) string {
	return jsonLine(t, entry)
}

// A project directory name turns every separator into a dash, so /work/my-app and /work/my/app
// share one name. Without an index entry, only the transcript knows which it was.
func TestClaudeSessionWithoutIndexUsesTheTranscriptCWD(t *testing.T) {
	projects := filepath.Join(t.TempDir(), "projects")
	path := filepath.Join(projects, "-work-my-app", "sess-dash.jsonl")
	writeFixture(t, path, strings.Join([]string{
		claudeLine(t, map[string]interface{}{"type": "queue-operation", "operation": "enqueue", "content": "ignored"}),
		claudeLine(t, map[string]interface{}{"type": "user", "cwd": "/work/my-app", "gitBranch": "main", "message": map[string]interface{}{"role": "user", "content": "<command-name>/clear</command-name>"}}),
		claudeLine(t, map[string]interface{}{"type": "user", "isMeta": true, "message": map[string]interface{}{"role": "user", "content": "caveat: meta"}}),
		claudeLine(t, map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "tool_result", "content": "not a prompt"},
		}}}),
		claudeLine(t, map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "image"},
			map[string]interface{}{"type": "text", "text": "  make the build\nfaster  "},
		}}}),
	}, ""))

	sessions, err := (&claudeSource{dir: projects}).List()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List = %+v, %v", sessions, err)
	}
	got := sessions[0]
	if got.Directory != "/work/my-app" {
		t.Fatalf("directory = %q, want the transcript cwd rather than the lossy directory-name decode", got.Directory)
	}
	if got.Branch != "main" || got.Title != "make the build faster" {
		t.Fatalf("branch/title = %q/%q", got.Branch, got.Title)
	}
}

func TestClaudeIndexProjectPathWinsOverTheTranscript(t *testing.T) {
	f := newStoreFixture(t)
	path := filepath.Join(f.dirs.ClaudeProjects, "-work-api", "claude-sess-1.jsonl")
	// The index says /work/api; a transcript cwd of a subdirectory the agent moved into must not
	// override it.
	writeFixture(t, path, claudeLine(t, map[string]interface{}{"type": "user", "cwd": "/work/api/cmd", "message": map[string]interface{}{"role": "user", "content": "hi"}}))
	sessions, err := (&claudeSource{dir: f.dirs.ClaudeProjects}).List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == "claude-sess-1" && s.Directory != "/work/api" {
			t.Fatalf("directory = %q, want the index projectPath", s.Directory)
		}
	}
}

func TestClaudeHeadSkipsAnOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	huge := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", claudeHeadMaxLineSize+10) + `"}}` + "\n"
	writeFixture(t, path, huge+claudeLine(t, map[string]interface{}{"type": "user", "cwd": "/after/huge", "message": map[string]interface{}{"role": "user", "content": "second line"}}))
	head := readClaudeHead(path)
	if head.CWD != "/after/huge" || head.FirstPrompt != "second line" {
		t.Fatalf("head = %+v; a line over the size cap must be skipped, not stop the read", head)
	}
}

func TestClaudeHeadStopsAfterTheLineBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	var b strings.Builder
	for i := 0; i < claudeHeadMaxLines; i++ {
		b.WriteString(`{"type":"attachment"}` + "\n")
	}
	b.WriteString(claudeLine(t, map[string]interface{}{"type": "user", "cwd": "/too/late"}))
	writeFixture(t, path, b.String())
	if head := readClaudeHead(path); head.CWD != "" {
		t.Fatalf("head read past its %d-line budget: %+v", claudeHeadMaxLines, head)
	}
}

func TestClaudeHeadToleratesMissingAndMalformedFiles(t *testing.T) {
	if head := readClaudeHead(filepath.Join(t.TempDir(), "missing.jsonl")); head != (claudeHead{}) {
		t.Fatalf("missing file head = %+v", head)
	}
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	writeFixture(t, path, "not json\n{\"type\":\"user\",\"cwd\":\"/ok\"}")
	if head := readClaudeHead(path); head.CWD != "/ok" {
		t.Fatalf("malformed line must be skipped and an unterminated last line read, got %+v", head)
	}
}

func TestReadBoundedLine(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("short\r\n"+strings.Repeat("y", 40)+"\nlast"), 16)
	for _, want := range []string{"short", "", "last"} {
		line, _ := readBoundedLine(r, 32)
		if string(line) != want {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}
}
