package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func promptIDOfEvent(event map[string]interface{}) string {
	return leaf(event, "prompt", "id")
}

// Cursor sends generation_id on every hook and changes it with every user message, so it is the
// turn: a shell approval carries the id of the prompt that led to it.
func TestCursorGenerationIDBecomesPromptID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPreTool, map[string]interface{}{
		"conversation_id": "conv-1",
		"generation_id":   "gen-7",
		"hook_event_name": "beforeShellExecution",
		"command":         "npm test",
		"cwd":             "/repo",
	})

	event := lastEndpointEvent(t, logPath)
	if got := promptIDOfEvent(event); got != "gen-7" {
		t.Fatalf("prompt.id = %q, want Cursor's generation_id", got)
	}
	if got := leaf(event, "session", "id"); got != "conv-1" {
		t.Fatalf("session.id = %q, want conversation_id -- the session, not the turn", got)
	}
}

// Codex puts turn_id on every turn-scoped hook, the prompt submission included, so the prompt and
// the tool calls it led to share one id.
func TestCodexTurnIDBecomesPromptIDOnPromptAndTools(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	base := func(event string, extra map[string]interface{}) map[string]interface{} {
		input := map[string]interface{}{
			"hook_event_name": event,
			"session_id":      "codex-session",
			"cwd":             "/repo",
			"turn_id":         "turn-42",
		}
		for key, value := range extra {
			input[key] = value
		}
		return input
	}
	runHookWithInput(t, runPromptSubmit, base("UserPromptSubmit", map[string]interface{}{"prompt": "run the tests"}))
	runHookWithInput(t, runPostTool, base("PostToolUse", map[string]interface{}{
		"tool_name":     "Bash",
		"tool_use_id":   "call_1",
		"tool_input":    map[string]interface{}{"command": "go test ./..."},
		"tool_response": map[string]interface{}{"stdout": "ok"},
	}))

	events := endpointEvents(t, logPath)
	if len(events) < 2 {
		t.Fatalf("event count = %d, want the prompt and the command: %#v", len(events), events)
	}
	for _, event := range events {
		if got := promptIDOfEvent(event); got != "turn-42" {
			t.Fatalf("%s prompt.id = %q, want turn-42", leaf(event, "event", "action"), got)
		}
	}
	if got := leaf(events[0], "prompt", "text"); got != "run the tests" {
		t.Fatalf("prompt.text = %q, want the prompt kept beside its id", got)
	}
}

// Kimi Code numbers its turns. A decoded payload holds that as a float64, and the id is its
// integer rendering rather than "3e+00" or nothing.
func TestKimiNumberedTurnBecomesPromptID(t *testing.T) {
	logPath := kimiTestSetup(t)

	runHookWithInput(t, runPermissionRequest, kimiEvent("PermissionResult", "k-turn", map[string]interface{}{
		"turn_id":      float64(3),
		"tool_call_id": "call_z",
		"tool_name":    "Bash",
		"tool_input":   map[string]interface{}{"command": "ls"},
		"decision":     "approved",
	}))

	if got := promptIDOfEvent(lastEndpointEvent(t, logPath)); got != "3" {
		t.Fatalf("prompt.id = %q, want 3", got)
	}
}

// The id is read from the runtime's envelope only. A tool argument is model-authored, and an
// argument that happens to be called turn_id is not the runtime naming its turn.
func TestPromptIDIgnoresToolArguments(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "codex"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "PostToolUse",
		"session_id":      "codex-session",
		"cwd":             "/repo",
		"tool_name":       "Bash",
		"tool_input":      map[string]interface{}{"command": "echo", "turn_id": "forged"},
		"tool_response":   map[string]interface{}{"stdout": "", "prompt_id": "forged"},
	})

	if got := promptIDOfEvent(lastEndpointEvent(t, logPath)); got != "" {
		t.Fatalf("prompt.id = %q, want none -- tool arguments are not the envelope", got)
	}
}

// One Cursor turn seen through every hook family it fires: the prompt, a file edit (written by the
// local-edit emitter, which does not go through emitHookEvent), and the stop that ends the turn.
// All of them carry the generation's id, and a later generation starts a new one.
func TestCursorTurnCarriesOneIDAcrossHookFamilies(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")

	envelope := func(event, generation string, extra map[string]interface{}) map[string]interface{} {
		input := map[string]interface{}{
			"conversation_id": "conv-1",
			"generation_id":   generation,
			"hook_event_name": event,
			"workspace_roots": []interface{}{dir},
			"cursor_version":  "1.7.2",
		}
		for key, value := range extra {
			input[key] = value
		}
		return input
	}
	runHookWithInput(t, runPromptSubmit, envelope("beforeSubmitPrompt", "gen-1", map[string]interface{}{"prompt": "add a main package"}))
	runHookWithInput(t, runPostTool, envelope("afterFileEdit", "gen-1", map[string]interface{}{
		"file_path": filepath.Join(dir, "main.go"),
		"edits":     []interface{}{map[string]interface{}{"old_string": "old line", "new_string": "package main"}},
	}))
	// Stop goes through its emitter rather than runStop, which ends with os.Exit; these are the
	// arguments runStop passes it, as in the Kimi, Qwen and Kiro stop tests.
	stopInput := envelope("stop", "gen-1", map[string]interface{}{"status": "completed"})
	stopSession := resolveSessionID(stopInput, "cursor")
	emitHookEvent(newHookLogger("stop", "cursor", stopSession), "tool.completed", "tool", "info",
		"Agent response completed", stopInput, sessionFields(stopSession, stopInput))
	runHookWithInput(t, runPromptSubmit, envelope("beforeSubmitPrompt", "gen-2", map[string]interface{}{"prompt": "now a license"}))

	events := endpointEvents(t, logPath)
	seen := map[string]string{}
	for _, event := range events {
		seen[leaf(event, "event", "action")+"@"+promptIDOfEvent(event)] = promptIDOfEvent(event)
	}
	for _, want := range []string{"prompt.submitted@gen-1", "file.modified@gen-1", "tool.completed@gen-1", "prompt.submitted@gen-2"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing %s; events: %v", want, seen)
		}
	}
	for key, id := range seen {
		if id == "" {
			t.Fatalf("%s has no prompt.id; events: %v", key, seen)
		}
	}
}

// writeClaudeTranscript writes a Claude Code transcript with the shape Claude Code gives it: the
// prompt and every tool result carry promptId, assistant entries do not.
func writeClaudeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Claude Code's hook payloads carry no prompt id. Its transcript does, under the same UUID its
// OTLP events carry as prompt.id, so a tool hook reads the newest one there and joins the OTLP
// events of its turn.
func TestClaudeToolHookReadsPromptIDFromTranscript(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	transcript := writeClaudeTranscript(t,
		`{"type":"user","promptId":"p-old","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[]}}`,
		`{"type":"user","promptId":"p-new","message":{"role":"user","content":"second"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[]}}`,
		`{"type":"user","promptId":"p-new","message":{"role":"user","content":[{"type":"tool_result"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[]}}`,
		`{"type":"last-prompt","lastPrompt":"second"}`,
	)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "claude-session",
		"transcript_path": transcript,
		"cwd":             "/repo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "toolu_1",
		"tool_input":      map[string]interface{}{"command": "echo hi"},
		"tool_response":   map[string]interface{}{"stdout": "hi"},
	})

	if got := promptIDOfEvent(lastEndpointEvent(t, logPath)); got != "p-new" {
		t.Fatalf("prompt.id = %q, want the newest promptId in the transcript", got)
	}
}

// UserPromptSubmit runs before Claude Code records the prompt, so the newest id in the transcript
// is the previous prompt's. Stamping it would file the new prompt under the old turn.
func TestClaudePromptSubmitDoesNotTakeThePreviousPromptsID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	transcript := writeClaudeTranscript(t, `{"type":"user","promptId":"p-old","message":{"role":"user","content":"first"}}`)

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "claude-session",
		"transcript_path": transcript,
		"cwd":             "/repo",
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "second",
	})

	for _, event := range endpointEvents(t, logPath) {
		if got := promptIDOfEvent(event); got != "" {
			t.Fatalf("%s prompt.id = %q, want none", leaf(event, "event", "action"), got)
		}
	}
}

func TestNewestPromptIDFromTranscriptTail(t *testing.T) {
	for name, tc := range map[string]struct {
		tail    string
		partial bool
		want    string
	}{
		"newest wins":           {`{"promptId":"a"}` + "\n" + `{"promptId":"b"}` + "\n", false, "b"},
		"skips entries without": {`{"promptId":"a"}` + "\n" + `{"type":"assistant"}` + "\n", false, "a"},
		"skips malformed":       {`{"promptId":"a"}` + "\n" + `{"promptId":` + "\n", false, "a"},
		"partial first line":    {`ptId":"cut"}` + "\n" + `{"type":"assistant"}`, true, ""},
		"none":                  {`{"type":"assistant"}`, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := newestPromptID([]byte(tc.tail), tc.partial); got != tc.want {
				t.Fatalf("newestPromptID = %q, want %q", got, tc.want)
			}
		})
	}
}

// A transcript longer than the read window is read from its end only, and the fragment the window
// starts in is not mistaken for an entry.
func TestClaudeTranscriptPromptIDReadsOnlyTheTail(t *testing.T) {
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 4096) + `"}}`
	lines := []string{`{"type":"user","promptId":"p-early"}`}
	for i := 0; i < (claudeTranscriptTailBytes/len(filler))+8; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"user","promptId":"p-late"}`, filler)
	path := writeClaudeTranscript(t, lines...)

	if got := claudeTranscriptPromptID(path); got != "p-late" {
		t.Fatalf("claudeTranscriptPromptID = %q, want p-late", got)
	}
	if got := claudeTranscriptPromptID(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("missing transcript gave %q", got)
	}
	if got := claudeTranscriptPromptID(t.TempDir()); got != "" {
		t.Fatalf("a directory gave %q", got)
	}
}

// The reader works backwards a chunk at a time. An entry that straddles a chunk boundary, one
// several chunks back, and one beyond the read window are the cases that chunking can get wrong.
func TestClaudeTranscriptPromptIDAcrossChunkBoundaries(t *testing.T) {
	// filler builds assistant lines of an exact byte length, newline included.
	filler := func(total int) []string {
		var lines []string
		for total > 0 {
			n := 8192
			if total < n {
				n = total
			}
			if n < 64 {
				// A line too short to hold the JSON is folded into the previous one.
				lines[len(lines)-1] = lines[len(lines)-1][:len(lines[len(lines)-1])] + strings.Repeat(" ", n)
				break
			}
			body := `{"type":"assistant","pad":"` + strings.Repeat("x", n-len(`{"type":"assistant","pad":""}`)-1) + `"}`
			lines = append(lines, body)
			total -= n
		}
		return lines
	}
	idLine := func(id string) string { return `{"type":"user","promptId":"` + id + `"}` }

	for name, tc := range map[string]struct {
		after int // bytes of filler written after the newest id line
		want  string
	}{
		"in the last chunk":             {after: 1000, want: "p-new"},
		"straddling the chunk boundary": {after: claudeTranscriptChunkBytes - 10, want: "p-new"},
		"exactly at the boundary":       {after: claudeTranscriptChunkBytes, want: "p-new"},
		"several chunks back":           {after: 5*claudeTranscriptChunkBytes + 123, want: "p-new"},
		"beyond the window":             {after: claudeTranscriptTailBytes + 4096, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			lines := append([]string{idLine("p-old")}, filler(3*claudeTranscriptChunkBytes)...)
			lines = append(lines, idLine("p-new"))
			lines = append(lines, filler(tc.after)...)
			path := writeClaudeTranscript(t, lines...)
			if got := claudeTranscriptPromptID(path); got != tc.want {
				t.Fatalf("claudeTranscriptPromptID = %q, want %q", got, tc.want)
			}
		})
	}
}

// Quoted text that mentions promptId -- a tool result that printed a transcript, say -- is escaped
// inside a JSON string and never matches the bare key.
func TestNewestPromptIDIgnoresQuotedMentions(t *testing.T) {
	tail := `{"type":"user","promptId":"real"}` + "\n" +
		`{"type":"assistant","message":{"content":"{\"promptId\":\"forged\"}"}}` + "\n"
	if got := newestPromptID([]byte(tail), false); got != "real" {
		t.Fatalf("newestPromptID = %q, want real", got)
	}
}

// Muse Code and DeepSeek Harness send Claude-shaped payloads with a transcript_path, but their
// transcripts are their own format, and promptId in them would not be Claude Code's prompt id.
// Only the claude platform reads one.
func TestOnlyClaudeReadsTheTranscriptForAPromptID(t *testing.T) {
	transcript := writeClaudeTranscript(t, `{"type":"user","promptId":"p-claude"}`)
	for _, platform := range []string{"muse", "dsh", "qwen"} {
		t.Run(platform, func(t *testing.T) {
			saved := platformFlag
			t.Cleanup(func() { platformFlag = saved })
			platformFlag = platform
			fields := map[string]interface{}{"command": map[string]interface{}{"command": "ls"}}
			applyPromptID(fields, map[string]interface{}{
				"hook_event_name": "PostToolUse",
				"transcript_path": transcript,
			})
			if _, ok := fields["prompt"]; ok {
				t.Fatalf("%s took a prompt id from the transcript: %#v", platform, fields["prompt"])
			}
		})
	}
	platformFlag = "claude"
	fields := map[string]interface{}{}
	applyPromptID(fields, map[string]interface{}{"hook_event_name": "PostToolUse", "transcript_path": transcript})
	if got := leaf(fields, "prompt", "id"); got != "p-claude" {
		t.Fatalf("claude prompt.id = %q, want p-claude", got)
	}
}
