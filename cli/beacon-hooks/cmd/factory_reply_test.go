package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// factoryRecord is one line of a Droid session transcript, in the shape factorysession reads.
func factoryRecord(id, role string, content interface{}) string {
	data, err := json.Marshal(map[string]interface{}{
		"type":      "message",
		"id":        id,
		"timestamp": "2026-10-15T10:00:00Z",
		"message":   map[string]interface{}{"role": role, "content": content},
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func factoryText(text string) map[string]interface{} {
	return map[string]interface{}{"type": "text", "text": text}
}

func factoryToolUse(id string) map[string]interface{} {
	return map[string]interface{}{"type": "tool_use", "id": id, "name": "Read", "input": map[string]interface{}{"file_path": "/repo/a.go"}}
}

func factoryToolResult(id string) map[string]interface{} {
	return map[string]interface{}{"type": "tool_result", "tool_use_id": id, "content": "package a"}
}

// factoryTurn is a whole Droid turn: the prompt, a tool call with intermediate text, its result,
// and the final answer in two text blocks.
func factoryTurn(prefix, final string) []string {
	return []string{
		factoryRecord(prefix+"-u", "user", []interface{}{factoryText("explain a.go")}),
		factoryRecord(prefix+"-a1", "assistant", []interface{}{factoryText("Let me read it."), factoryToolUse(prefix + "-t")}),
		factoryRecord(prefix+"-r", "user", []interface{}{factoryToolResult(prefix + "-t")}),
		factoryRecord(prefix+"-a2", "assistant", []interface{}{factoryText(final), factoryText("Anything else?")}),
	}
}

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// factoryStopPayload is Droid's documented Stop input: the common fields plus stop_hook_active,
// tool_execution_count and elapsed_time. No field carries the reply.
func factoryStopPayload(transcriptPath string) map[string]interface{} {
	return map[string]interface{}{
		"session_id":           "droid-1",
		"transcript_path":      transcriptPath,
		"cwd":                  "/repo",
		"permission_mode":      "auto-low",
		"hook_event_name":      "Stop",
		"stop_hook_active":     false,
		"tool_execution_count": 1,
		"elapsed_time":         4200,
	}
}

func TestFactoryTranscriptReplyReadsTheFinalAssistantMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "droid-1.jsonl")
	lines := append([]string{`{"type":"session_start","id":"droid-1","cwd":"/repo"}`}, factoryTurn("t1", "a.go declares package a.")...)
	lines = append(lines, `{"type":"todo_state","id":"todo-1"}`)
	writeTranscript(t, path, lines...)

	reply, ok := factoryTranscriptReply(path)
	if !ok {
		t.Fatal("no reply read from a finished turn")
	}
	if reply.id != "t1-a2" || reply.text != "a.go declares package a.\n\nAnything else?" {
		t.Fatalf("reply = %#v, want t1-a2 with both text blocks", reply)
	}
}

// The transcript can lag the Stop hook. Every case below is a turn the transcript has not finished
// recording, and none of them may be reported as the answer.
func TestFactoryTranscriptReplyRefusesAnUnfinishedTurn(t *testing.T) {
	turn := factoryTurn("t1", "done")
	cases := map[string][]string{
		"ends on a tool call":            turn[:2],
		"ends on a tool result":          turn[:3],
		"text-only block list is blank":  {turn[0], factoryRecord("x", "assistant", []interface{}{factoryText("  ")})},
		"assistant message with no body": {turn[0], `{"type":"message","id":"x","message":{"role":"assistant"}}`},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTranscript(t, path, lines...)
			if reply, ok := factoryTranscriptReply(path); ok {
				t.Fatalf("reported %#v as the answer of an unfinished turn", reply)
			}
		})
	}
}

// A message hidden from the user or from the model is never the reply, matching the session sync,
// which drops user_only messages and records llm_only ones as errors. It is refused, not skipped,
// so the visible message before it is not reported in its place.
func TestFactoryTranscriptReplyRefusesHiddenMessages(t *testing.T) {
	for _, visibility := range []string{"user_only", "llm_only"} {
		t.Run(visibility, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			hidden, err := json.Marshal(map[string]interface{}{
				"type": "message",
				"id":   "hidden",
				"message": map[string]interface{}{
					"role":       "assistant",
					"visibility": visibility,
					"content":    []interface{}{factoryText("internal notice")},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			writeTranscript(t, path, append(factoryTurn("t1", "visible answer"), string(hidden))...)
			if reply, ok := factoryTranscriptReply(path); ok {
				t.Fatalf("reported %#v; a %s message is not the reply", reply, visibility)
			}
		})
	}
}

// A record still being written is skipped, and the newest complete record decides.
func TestFactoryTranscriptReplySkipsAPartialLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	turn := factoryTurn("t1", "done")
	data := strings.Join(turn[:3], "\n") + "\n" + `{"type":"message","id":"t1-a2","message":{"role":"assis`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if reply, ok := factoryTranscriptReply(path); ok {
		t.Fatalf("reported %#v while the final record was still being written", reply)
	}
}

func TestFactoryTranscriptReplyAcceptsAStringBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, factoryRecord("u", "user", "hi"), factoryRecord("a", "assistant", "Hello."))
	if reply, ok := factoryTranscriptReply(path); !ok || reply.text != "Hello." {
		t.Fatalf("reply = %#v, %v; want the string body", reply, ok)
	}
}

// Only the tail of a long session is read, and the reply at its end is still found.
func TestFactoryTranscriptReplyReadsOnlyTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	filler := factoryRecord("old", "assistant", []interface{}{factoryText(strings.Repeat("y", 4096))})
	var lines []string
	for i := 0; i < (factoryTranscriptTailBytes/len(filler))*2; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines, factoryTurn("t9", "the newest answer")...)
	writeTranscript(t, path, lines...)
	if reply, ok := factoryTranscriptReply(path); !ok || reply.id != "t9-a2" {
		t.Fatalf("reply = %#v, %v; want t9-a2", reply, ok)
	}
}

func TestFactoryTranscriptReplyRefusesWhatIsNotATranscript(t *testing.T) {
	dir := t.TempDir()
	notJSONL := filepath.Join(dir, "s.json")
	writeTranscript(t, notJSONL, factoryTurn("t1", "x")...)
	for name, path := range map[string]string{"missing": filepath.Join(dir, "none.jsonl"), "wrong extension": notJSONL, "directory": dir + ".jsonl", "empty": ""} {
		if name == "directory" {
			_ = os.Mkdir(path, 0o700)
		}
		if reply, ok := factoryTranscriptReply(path); ok {
			t.Fatalf("%s: read %#v", name, reply)
		}
	}
}

// End to end through the real stop command: the reply is recorded once, ahead of the turn's
// completion, and a second Stop over the same transcript does not record it again. The next turn's
// reply is recorded.
func TestFactoryStopRecordsTheTranscriptReplyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "droid-1.jsonl")
	turn1 := factoryTurn("t1", "a.go declares package a.")
	writeTranscript(t, path, turn1...)

	logPath, stdout := runHookProcessIn(t, dir, factoryStopPayload(path), "--platform", "factory", "stop")
	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stdout = %q, want {}", stdout)
	}
	messages := assistantMessages(t, logPath)
	if len(messages) != 1 {
		t.Fatalf("got %d agent.message events, want 1", len(messages))
	}
	if got := outputText(t, messages[0]); got != "a.go declares package a.\n\nAnything else?" {
		t.Fatalf("reply = %q", got)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"harness", "name"}, "factory"},
		{[]string{"harness", "collection_method"}, "hook"},
		{[]string{"session", "id"}, "droid-1"},
		{[]string{"raw", "factory", "reply_message_id"}, "t1-a2"},
	} {
		if got := leaf(messages[0], check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}
	if last := lastEndpointEvent(t, logPath); leaf(last, "event", "action") != "tool.completed" {
		t.Fatalf("last event = %q, want the turn completion after the reply", leaf(last, "event", "action"))
	}

	runHookProcessIn(t, dir, factoryStopPayload(path), "--platform", "factory", "stop")
	if n := len(assistantMessages(t, logPath)); n != 1 {
		t.Fatalf("an unchanged transcript recorded the reply %d times, want once", n)
	}

	writeTranscript(t, path, append(turn1, factoryTurn("t2", "Second answer.")...)...)
	runHookProcessIn(t, dir, factoryStopPayload(path), "--platform", "factory", "stop")
	messages = assistantMessages(t, logPath)
	if len(messages) != 2 || leaf(messages[1], "raw", "factory", "reply_message_id") != "t2-a2" {
		t.Fatalf("second turn's reply not recorded: %d messages", len(messages))
	}
}

// The transcript read is Droid's alone: a Claude-shaped runtime whose Stop names a transcript in
// the same format records nothing from it.
func TestTranscriptReplyIsReadOnlyForFactory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeTranscript(t, path, factoryTurn("t1", "x")...)
	for _, platform := range []string{"devin-cli"} {
		logPath, _ := runHookProcessIn(t, t.TempDir(), factoryStopPayload(path), "--platform", platform, "stop")
		if messages := assistantMessages(t, logPath); len(messages) != 0 {
			t.Fatalf("%s read a reply from the transcript: %#v", platform, messages)
		}
	}
}
