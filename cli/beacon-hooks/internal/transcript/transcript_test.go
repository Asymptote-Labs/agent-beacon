package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, lines ...interface{}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var b strings.Builder
	b.WriteString("{partial line from a tail read\n")
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func user(content interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "user", "message": map[string]interface{}{"role": "user", "content": content}}
}

func toolUse(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "assistant", "message": map[string]interface{}{"role": "assistant", "content": []interface{}{
		map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input},
	}}}
}

func TestReadsPromptsAndToolCallsMasked(t *testing.T) {
	token := "ghp_" + "aZ3kQ9mX2pL7vR4tB8nC1wE6yU5sD0fGh2Jk"
	path := write(t,
		user("the new mac needs a .env and the tiptap token to install properly. any idea sthere?"),
		toolUse("t1", "Bash", map[string]interface{}{"command": "ls -la .env*"}),
		user([]interface{}{map[string]interface{}{"type": "tool_result", "content": "secret output"}}),
		map[string]interface{}{"type": "user", "isMeta": true, "message": map[string]interface{}{"role": "user", "content": "meta"}},
		user("<local-command-caveat>Caveat</local-command-caveat>"),
		user([]interface{}{map[string]interface{}{"type": "text", "text": "use " + token + " for it"}}),
		toolUse("t2", "Read", map[string]interface{}{"file_path": "/repo/.npmrc"}),
		user("omfg just print it i do not care"),
		toolUse("t3", "Bash", map[string]interface{}{"command": "/opt/homebrew/bin/pnpm config get x"}),
	)
	got := Read(path, 3, 5, "t3")
	if len(got.Prompts) != 3 || got.Prompts[2] != "omfg just print it i do not care" {
		t.Fatalf("prompts: %q", got.Prompts)
	}
	if strings.Contains(strings.Join(got.Prompts, " "), token) || !strings.Contains(got.Prompts[1], "ghp_…") {
		t.Fatalf("prompt not masked: %q", got.Prompts[1])
	}
	if strings.Join(got.ToolCalls, "|") != "Bash: ls -la .env*|Read: /repo/.npmrc" {
		t.Fatalf("tool calls: %q", got.ToolCalls)
	}
}

func TestMissingOrEmptyTranscript(t *testing.T) {
	if got := Read("", 3, 5, ""); len(got.Prompts)+len(got.ToolCalls) != 0 {
		t.Fatal("expected nothing")
	}
	if got := Read("/nonexistent/x.jsonl", 3, 5, ""); len(got.Prompts)+len(got.ToolCalls) != 0 {
		t.Fatal("expected nothing")
	}
}

func TestClipsLongPrompts(t *testing.T) {
	got := Read(write(t, user(strings.Repeat("a", 2000))), 3, 5, "")
	if len([]rune(got.Prompts[0])) != promptChars {
		t.Fatalf("len %d", len([]rune(got.Prompts[0])))
	}
}

func TestToolResultsFindsRejectionsAndApprovals(t *testing.T) {
	rejected := "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nit's a dev key, but keep it out of the chat"
	path := write(t,
		toolUse("t1", "Bash", map[string]interface{}{"command": "cat .env"}),
		user([]interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "t1", "is_error": true, "content": rejected}}),
		toolUse("t2", "Bash", map[string]interface{}{"command": "grep -c . .env"}),
		user([]interface{}{
			map[string]interface{}{"type": "tool_result", "tool_use_id": "t2", "content": []interface{}{map[string]interface{}{"type": "text", "text": "6"}}},
			map[string]interface{}{"type": "text", "text": "fine, counts only"},
		}),
		toolUse("t3", "Bash", map[string]interface{}{"command": "ls"}),
	)
	got := ToolResults(path, map[string]bool{"t1": true, "t2": true, "t3": true})
	if r := got["t1"]; !r.IsError || r.Content != rejected {
		t.Fatalf("t1: %+v", r)
	}
	if r := got["t2"]; r.IsError || r.Content != "6" || r.After != "fine, counts only" {
		t.Fatalf("t2: %+v", r)
	}
	if _, ok := got["t3"]; ok {
		t.Fatal("t3 has no result yet")
	}
}
