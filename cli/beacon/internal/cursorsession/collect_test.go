package cursorsession

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectGlobalStorageComposer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	writeCursorDB(t, dbPath, map[string]string{
		"composerData:composer-1": `{
			"composerId":"composer-1",
			"name":"Investigate build",
			"createdAt":"2026-09-19T20:00:00Z",
			"lastUpdatedAt":"2026-09-19T20:00:03Z",
			"modelConfig":{"modelName":"gpt-5.5"},
			"fullConversationHeadersOnly":[
				{"bubbleId":"u1","type":1},
				{"bubbleId":"a1","type":2},
				{"bubbleId":"t1","type":2}
			]
		}`,
		"bubbleId:composer-1:u1": `{"type":1,"text":"run the tests","createdAt":"2026-09-19T20:00:00Z"}`,
		"bubbleId:composer-1:a1": `{"type":2,"text":"Tests are green","createdAt":"2026-09-19T20:00:01Z"}`,
		"bubbleId:composer-1:t1": `{
			"type":2,
			"createdAt":"2026-09-19T20:00:02Z",
			"toolFormerData":{"name":"run_terminal_cmd","params":"{\"command\":\"go test ./...\"}","result":"ok","status":"success"}
		}`,
	})

	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{
		GlobalDBPath: dbPath,
		ProjectsDir:  filepath.Join(t.TempDir(), "missing-projects"),
		Print:        true,
		Out:          &out,
	})
	if err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if summary.EventsEmitted != 4 {
		t.Fatalf("events emitted = %d, want 4\n%s", summary.EventsEmitted, out.String())
	}
	events := decodeEvents(t, out.String())
	if actions := eventActions(events); strings.Join(actions, ",") != "prompt.submitted,agent.message,tool.invoked,command.executed" {
		t.Fatalf("actions = %v", actions)
	}
	command := events[3]["command"].(map[string]interface{})
	if command["command"] != "go test ./..." {
		t.Fatalf("command = %#v", command)
	}
	if got := nested(events[3], "harness")["collection_method"]; got != "poll" {
		t.Fatalf("collection method = %v, want poll", got)
	}
}

func TestCollectTranscriptJSONL(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-tmp-cursor-project", "agent-transcripts")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "session-1.jsonl"), []byte(strings.Join([]string{
		`{"type":"user_message","id":"u1","text":"hello","timestamp":"2026-09-19T20:00:00Z"}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"reasoning","text":"thinking"},{"type":"text","text":"hi"}],"model":"claude-sonnet-4"},"timestamp":"2026-09-19T20:00:01Z"}`,
		`{"type":"tool_call","id":"c1","toolCall":{"id":"call-1","name":"read_file","args":{"path":"/tmp/a.txt"}},"timestamp":"2026-09-19T20:00:02Z"}`,
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	summary, err := CollectOnce(CollectOptions{
		GlobalDBPath: filepath.Join(t.TempDir(), "missing.vscdb"),
		ProjectsDir:  root,
		Print:        true,
		Out:          &out,
	})
	if err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if summary.EventsEmitted != 4 {
		t.Fatalf("events emitted = %d, want 4\n%s", summary.EventsEmitted, out.String())
	}
	events := decodeEvents(t, out.String())
	if actions := eventActions(events); strings.Join(actions, ",") != "prompt.submitted,agent.reasoning,agent.message,tool.invoked" {
		t.Fatalf("actions = %v", actions)
	}
	if got := nested(events[1], "gen_ai", "output")["messages"]; got == nil {
		t.Fatalf("reasoning event missing gen_ai output: %#v", events[1])
	}
}

func writeCursorDB(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	for key, value := range kv {
		if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES (?, ?)`, key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
}

func decodeEvents(t *testing.T, text string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v\n%s", err, line)
		}
		out = append(out, event)
	}
	return out
}

func eventActions(events []map[string]interface{}) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, nested(event, "event")["action"].(string))
	}
	return out
}

func nested(root map[string]interface{}, keys ...string) map[string]interface{} {
	current := root
	for _, key := range keys {
		next, _ := current[key].(map[string]interface{})
		current = next
	}
	return current
}
