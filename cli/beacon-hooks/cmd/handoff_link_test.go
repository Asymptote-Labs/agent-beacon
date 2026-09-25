package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const handoffTestPrompt = "Continue the work from an earlier Claude Code session. Beacon wrote a handoff brief of it at /home/me/.beacon/endpoint/handoffs/b.md.\n\n[beacon-handoff from=claude_code session=src-session-1]"

func eventsWithAction(t *testing.T, logPath, action string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			out = append(out, event)
		}
	}
	return out
}

func assertHandoffLink(t *testing.T, event map[string]interface{}, sessionID, harness string) {
	t.Helper()
	handoff, _ := event["handoff"].(map[string]interface{})
	if handoff["source_harness"] != "claude_code" || handoff["source_session_id"] != "src-session-1" {
		t.Fatalf("handoff = %#v", event["handoff"])
	}
	session, _ := event["session"].(map[string]interface{})
	if session["id"] != sessionID {
		t.Fatalf("session.id = %v, want the new session %s", session["id"], sessionID)
	}
	meta, _ := event["event"].(map[string]interface{})
	if meta["category"] != "session" || meta["fidelity"] != asymptoteobserve.FidelityObserved {
		t.Fatalf("event = %#v, want an observed session event", meta)
	}
	if h, _ := event["harness"].(map[string]interface{}); h["name"] != harness {
		t.Fatalf("harness = %#v, want %s", event["harness"], harness)
	}
	for _, key := range []string{"prompt", "content"} {
		if _, ok := event[key]; ok {
			t.Fatalf("the link must not repeat the prompt's %s: %#v", key, event[key])
		}
	}
	if genAI, ok := event["gen_ai"].(map[string]interface{}); ok && genAI["input"] != nil {
		t.Fatalf("the link must not repeat the prompt text: %#v", genAI)
	}
}

func TestPromptSubmitRecordsAHandoffLink(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id": "new-session-1",
		"cwd":        "/work/api",
		"prompt":     handoffTestPrompt,
	})

	if got := len(eventsWithAction(t, logPath, "prompt.submitted")); got != 1 {
		t.Fatalf("prompt events = %d, want the prompt recorded as usual", got)
	}
	links := eventsWithAction(t, logPath, "session.handoff")
	if len(links) != 1 {
		t.Fatalf("session.handoff events = %d, want 1", len(links))
	}
	assertHandoffLink(t, links[0], "new-session-1", "claude_code")
	if session, _ := links[0]["session"].(map[string]interface{}); session["working_directory"] != "/work/api" {
		t.Fatalf("the link keeps the prompt's workspace: %#v", links[0]["session"])
	}
}

func TestPromptSubmitWithoutAMarkerRecordsNoLink(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	for _, prompt := range []string{"fix the tests", "[beacon-handoff from=claude_code]", ""} {
		runHookWithInput(t, runPromptSubmit, map[string]interface{}{"session_id": "s-2", "prompt": prompt})
	}
	if links := eventsWithAction(t, logPath, "session.handoff"); len(links) != 0 {
		t.Fatalf("session.handoff without a marker: %#v", links)
	}
}

func TestClinePromptRecordsAHandoffLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input map[string]interface{}
	}{
		{"prompt hook", map[string]interface{}{"hookName": "UserPromptSubmit", "taskId": "cline-new-1", "prompt": handoffTestPrompt}},
		{"run start", map[string]interface{}{"type": "runStart", "taskId": "cline-new-1", "prompt": handoffTestPrompt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := clineTestLog(t)
			runHookWithInput(t, runClineEvent, tc.input)
			links := eventsWithAction(t, logPath, "session.handoff")
			if len(links) != 1 {
				t.Fatalf("actions = %v, want one session.handoff", clineEventActions(t, logPath))
			}
			assertHandoffLink(t, links[0], "cline-new-1", "cline")
			if len(eventsWithAction(t, logPath, "prompt.submitted")) != 1 {
				t.Fatalf("actions = %v, want the prompt recorded too", clineEventActions(t, logPath))
			}
		})
	}
}

func TestOpenCodePromptRecordsAHandoffLink(t *testing.T) {
	events := opencodeEndpointEvents(map[string]interface{}{
		"type": "chat.message", "sessionID": "ses_new", "prompt": handoffTestPrompt,
	}, "ses_new")
	if len(events) != 2 || events[0].action != "prompt.submitted" || events[1].action != "session.handoff" {
		t.Fatalf("events = %+v", events)
	}
	handoff, _ := events[1].fields["handoff"].(map[string]interface{})
	if handoff["source_session_id"] != "src-session-1" {
		t.Fatalf("handoff = %#v", events[1].fields["handoff"])
	}
	if _, ok := events[1].fields["prompt"]; ok {
		t.Fatal("the link must not repeat the prompt")
	}
	if _, ok := events[0].fields["prompt"]; !ok {
		t.Fatal("building the link must not strip the prompt event's own fields")
	}

	plain := opencodeEndpointEvents(map[string]interface{}{"type": "chat.message", "prompt": "hello"}, "ses_new")
	if len(plain) != 1 {
		t.Fatalf("a prompt without a marker yields only the prompt event, got %+v", plain)
	}
}

// assertLinkedEvents checks a mapper's output for a marker prompt: the prompt event, then the link,
// with the prompt event's own fields left intact.
func assertLinkedEvents(t *testing.T, events []normalizedEvent) {
	t.Helper()
	if len(events) != 2 || events[0].action != "prompt.submitted" || events[1].action != "session.handoff" {
		t.Fatalf("events = %+v, want prompt.submitted then session.handoff", events)
	}
	handoff, _ := events[1].fields["handoff"].(map[string]interface{})
	if handoff["source_harness"] != "claude_code" || handoff["source_session_id"] != "src-session-1" {
		t.Fatalf("handoff = %#v", events[1].fields["handoff"])
	}
	if _, ok := events[1].fields["prompt"]; ok {
		t.Fatal("the link must not repeat the prompt")
	}
	if _, ok := events[0].fields["prompt"]; !ok {
		t.Fatal("building the link must not strip the prompt event's own fields")
	}
}

func TestPiFamilyPromptRecordsAHandoffLink(t *testing.T) {
	for _, runtime := range []piFamily{piRuntime, ompRuntime, primeRuntime, omoRuntime} {
		t.Run(runtime.platform, func(t *testing.T) {
			events := runtime.endpointEvents(map[string]interface{}{
				"type": "input", "text": handoffTestPrompt, "sessionId": "pi-new-1",
			}, "pi-new-1")
			assertLinkedEvents(t, events)
			plain := runtime.endpointEvents(map[string]interface{}{"type": "input", "text": "hello"}, "pi-new-1")
			if len(plain) != 1 {
				t.Fatalf("a prompt without a marker yields only the prompt event, got %+v", plain)
			}
		})
	}

	// End to end through the hook, so the link carries the runtime's harness and session.
	logPath := piTestLog(t)
	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type": "input", "text": handoffTestPrompt, "sessionId": "pi-new-1", "cwd": "/repo",
	})
	links := eventsWithAction(t, logPath, "session.handoff")
	if len(links) != 1 {
		t.Fatalf("actions = %v, want one session.handoff", piEventActions(t, logPath))
	}
	assertHandoffLink(t, links[0], "pi-new-1", "pi_cli")
}

func TestOpenClawPromptRecordsAHandoffLink(t *testing.T) {
	assertLinkedEvents(t, openClawEvents(t, "message_received", map[string]interface{}{
		"content": handoffTestPrompt, "from": "U42",
	}, nil))
	if got := openClawEvents(t, "message_received", map[string]interface{}{"content": "deploy staging"}, nil); len(got) != 1 {
		t.Fatalf("a prompt without a marker yields only the prompt event, got %+v", got)
	}
}

// Antigravity's prompt hook does not always fire; pre-tool then recovers the prompt from the
// transcript, and that recovered prompt must link too.
func TestAntigravityTranscriptPromptRecordsAHandoffLink(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "antigravity"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	line, err := json.Marshal(map[string]interface{}{
		"source": "USER_EXPLICIT", "type": "USER_INPUT",
		"content": "<USER_REQUEST>\n" + handoffTestPrompt + "\n</USER_REQUEST>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	input := map[string]interface{}{
		"conversationId": "ag-new-1",
		"transcriptPath": transcriptPath,
		"toolCall":       map[string]interface{}{"name": "list_dir", "args": map[string]interface{}{}},
	}
	runHookWithInput(t, runPreTool, input)
	runHookWithInput(t, runPreTool, input)

	links := eventsWithAction(t, logPath, "session.handoff")
	if len(links) != 1 {
		t.Fatalf("session.handoff events = %d, want 1 however many tools run", len(links))
	}
	assertHandoffLink(t, links[0], "ag-new-1", "antigravity_cli")
}
