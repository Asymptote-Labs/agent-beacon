package groksession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func TestMapSessionRecordsAHandoffLink(t *testing.T) {
	prompt := "Continue the work.\n\n" + asymptoteobserve.HandoffMarker("claude_code", "src-1")
	marked, _ := json.Marshal([]map[string]string{{"type": "text", "text": prompt}})
	plain, _ := json.Marshal("no marker here")
	data := SessionData{
		Ref: SessionRef{ID: "new-1", Workspace: "/work", SourcePath: "/tmp/grok/new-1"},
		Chat: []ChatMessage{
			{Type: "user", Content: marked, Index: 1},
			{Type: "user", Content: plain, Index: 2},
		},
	}
	mapped := MapSession(data, MapOptions{})
	var links []MappedEvent
	for _, m := range mapped {
		if m.Event.Event.Action == "session.handoff" {
			links = append(links, m)
		}
	}
	if len(links) != 1 {
		t.Fatalf("links = %d, want 1: %+v", len(links), mapped)
	}
	link := links[0]
	if link.SourceKind != SourceChat || link.SourceLine != 1 || link.Event.Handoff == nil || link.Event.Handoff.SourceHarness != "claude_code" || link.Event.Handoff.SourceSessionID != "src-1" {
		t.Fatalf("link = %+v handoff=%+v", link, link.Event.Handoff)
	}
	if link.Event.Session == nil || link.Event.Session.ID != "new-1" || link.Event.Session.WorkingDirectory != "/work" {
		t.Fatalf("link session = %+v", link.Event.Session)
	}
	if link.Event.Harness.Name != Harness || link.Event.Harness.CollectionMethod != "poll" || link.Event.Event.Fidelity != "observed" {
		t.Fatalf("link provenance = %+v %+v", link.Event.Harness, link.Event.Event)
	}
	if link.Event.Prompt != nil || link.Event.Content != nil || link.Event.GenAI != nil {
		t.Fatalf("the link must not repeat the prompt: %+v", link.Event)
	}
	// Every other Grok event is named by the writer from its bytes; the link names itself, from
	// where it was read, because its bytes carry the time of the sync.
	if link.Event.Event.ID == "" {
		t.Fatal("the link has no id")
	}
	again := MapSession(data, MapOptions{})
	for _, m := range again {
		if m.Event.Event.Action == "session.handoff" && m.Event.Event.ID != link.Event.Event.ID {
			t.Fatal("the link's id must be deterministic, so reading the prompt again names the same link")
		}
	}
	other := data
	other.Ref.ID = "new-2"
	for _, m := range MapSession(other, MapOptions{}) {
		if m.Event.Event.Action == "session.handoff" && m.Event.Event.ID == link.Event.Event.ID {
			t.Fatal("links in different sessions share an id")
		}
	}
}

// The collector's cursor is what keeps a second sync from writing the link again; when the cursor
// is lost, the link written again carries the same event.id.
func TestCollectOnceWritesTheHandoffLinkOnce(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeFixtureSession(t, root, "/tmp/grok project", "session-1")
	marker := asymptoteobserve.HandoffMarker("codex_cli", "019a-thread")
	appendLine(t, filepath.Join(sessionDir, "chat_history.jsonl"),
		`{"type":"user","content":[{"type":"text","text":"Read the brief.\n\n`+marker+`"}]}`)
	statePath := filepath.Join(t.TempDir(), "state.json")
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	opts := CollectOptions{SessionsDir: root, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}

	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("first CollectOnce: %v", err)
	}
	touchNewer(t, sessionDir)
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second CollectOnce: %v", err)
	}
	if got := logActions(t, logPath)["session.handoff"]; got != 1 {
		t.Fatalf("session.handoff written %d times, want 1", got)
	}

	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("CollectOnce without state: %v", err)
	}
	ids := linkIDs(t, logPath)
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("link ids = %q, want the same id both times", ids)
	}
}

func linkIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Event struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Event.Action == "session.handoff" {
			ids = append(ids, record.Event.ID)
		}
	}
	return ids
}
