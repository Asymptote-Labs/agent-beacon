package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// hookProcessArgsEnv carries the command line TestHookProcess runs, as a JSON array.
const hookProcessArgsEnv = "BEACON_HOOKS_TEST_PROCESS_ARGS"

// TestHookProcess is not a test. runHookProcess re-executes the test binary into it to run one hook
// command end to end, because the hook commands end with outputJSONAndExit and cannot be called
// in-process without ending the test run.
func TestHookProcess(t *testing.T) {
	raw := os.Getenv(hookProcessArgsEnv)
	if raw == "" {
		t.Skip("helper process for runHookProcess")
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		os.Exit(3)
	}
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

// runHookProcess runs `beacon-hooks <args...>` in a child process with input on stdin, the way a
// runtime invokes it, and returns the endpoint log it wrote and what it printed. The child gets a
// home directory of its own, so no hook state or session record reaches the real one.
func runHookProcess(t *testing.T, input map[string]interface{}, args ...string) (logPath string, stdout []byte) {
	t.Helper()
	return runHookProcessIn(t, t.TempDir(), input, args...)
}

// runHookProcessIn is runHookProcess with the log and home directory under dir, so consecutive
// hooks of one session share their log and hook state.
func runHookProcessIn(t *testing.T, dir string, input map[string]interface{}, args ...string) (logPath string, stdout []byte) {
	t.Helper()
	logPath = filepath.Join(dir, "runtime.jsonl")
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookProcess$")
	cmd.Env = append(os.Environ(),
		hookProcessArgsEnv+"="+string(encodedArgs),
		"HOME="+home,
		"USERPROFILE="+home,
		"BEACON_ENDPOINT_MODE=1",
		"BEACON_ENDPOINT_LOG="+logPath,
		"BEACON_DISABLE_GIT_METADATA=1",
	)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("beacon-hooks %v: %v\nstderr: %s", args, err, errOut.String())
	}
	return logPath, out.Bytes()
}

// assistantMessages returns the agent.message events in a log, in order.
func assistantMessages(t *testing.T, logPath string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, event := range endpointEvents(t, logPath) {
		if leaf(event, "event", "action") == "agent.message" {
			out = append(out, event)
		}
	}
	return out
}

// outputText is the assistant text an event carries in gen_ai.output.messages, read with the same
// reader the trace projection uses.
func outputText(t *testing.T, event map[string]interface{}) string {
	t.Helper()
	genAI, _ := event["gen_ai"].(map[string]interface{})
	output, _ := genAI["output"].(map[string]interface{})
	return strings.Join(asymptoteobserve.GenAIText(output["messages"], asymptoteobserve.RoleAssistant, asymptoteobserve.GenAIPartTypeText), "\n\n")
}

// chatView projects a log the way the dashboard and beacon share do, and returns its messages.
func chatView(t *testing.T, logPath string) []asymptoteobserve.TraceMessageV1 {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle asymptoteobserve.TraceBundleV1
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event asymptoteobserve.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line %d: %v", i+1, err)
		}
		bundle.Events = append(bundle.Events, asymptoteobserve.TraceEventFromEvent(event, "", i+1))
	}
	return asymptoteobserve.TraceMessages(bundle)
}

// claudeStopPayload is Claude Code's documented Stop input.
func claudeStopPayload(lastMessage interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"session_id":       "claude-stop-1",
		"prompt_id":        "prompt-7",
		"transcript_path":  "/nonexistent/claude-stop-1.jsonl",
		"cwd":              "/repo",
		"permission_mode":  "default",
		"hook_event_name":  "Stop",
		"stop_hook_active": false,
	}
	if lastMessage != nil {
		payload["last_assistant_message"] = lastMessage
	}
	return payload
}

// Claude Code's Stop payload carries the turn's final reply. Before this, the hook recorded that the
// turn ended and dropped what was said, so a live Claude Code session had prompts and tool calls
// but no answers until someone ran `beacon endpoint claude sync`.
func TestClaudeStopRecordsTheFinalAssistantMessage(t *testing.T) {
	reply := "I've completed the refactoring.\n\nThe tests pass."
	logPath, stdout := runHookProcess(t, claudeStopPayload(reply), "--platform", "claude", "stop")

	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stop printed %q, want the {} no-op reply", stdout)
	}
	events := endpointEvents(t, logPath)
	messages := assistantMessages(t, logPath)
	if len(messages) != 1 {
		t.Fatalf("got %d agent.message events, want 1: %#v", len(messages), events)
	}
	message := messages[0]
	if got := outputText(t, message); got != reply {
		t.Fatalf("gen_ai.output.messages text = %q, want the reply verbatim", got)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"event", "category"}, "session"},
		{[]string{"event", "fidelity"}, asymptoteobserve.FidelityObserved},
		{[]string{"harness", "name"}, "claude_code"},
		{[]string{"harness", "collection_method"}, asymptoteobserve.CollectionMethodHook},
		{[]string{"session", "id"}, "claude-stop-1"},
		{[]string{"prompt", "id"}, "prompt-7"},
	} {
		if got := leaf(message, check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}
	content, ok := message["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("no content marker: %#v", message)
	}
	if content["bytes"] != float64(len(reply)) || content["included"] != true || content["hash"] == "" {
		t.Fatalf("content marker = %#v, want the reply's size and hash", content)
	}

	// The reply precedes the turn's completion, because it happened first.
	if last := events[len(events)-1]; leaf(last, "event", "action") != "tool.completed" {
		t.Fatalf("last event = %q, want the turn completion after the reply", leaf(last, "event", "action"))
	}

	// What the dashboard and beacon share show: the reply is an assistant message, not a label.
	var texts []string
	for _, m := range chatView(t, logPath) {
		if m.Role == asymptoteobserve.TraceRoleAssistant && m.Kind == asymptoteobserve.TraceMessageKindAssistant {
			texts = append(texts, m.Text)
		}
	}
	if len(texts) != 1 || texts[0] != reply {
		t.Fatalf("chat view assistant text = %q, want exactly the reply", texts)
	}
}

// A turn can end without a reply: on a tool call, an interrupt, or a field the runtime left null.
// That is not an empty answer, so no message event is written -- but the turn's completion still is.
func TestClaudeStopWithoutAReplyWritesNoMessage(t *testing.T) {
	for name, value := range map[string]interface{}{
		"null":  json.RawMessage("null"),
		"blank": "  \n\t",
	} {
		t.Run(name, func(t *testing.T) {
			logPath, _ := runHookProcess(t, claudeStopPayload(value), "--platform", "claude", "stop")
			if messages := assistantMessages(t, logPath); len(messages) != 0 {
				t.Fatalf("wrote agent.message for a Stop with no reply: %#v", messages)
			}
			if got := leaf(lastEndpointEvent(t, logPath), "event", "action"); got != "tool.completed" {
				t.Fatalf("last event = %q, want the turn completion", got)
			}
		})
	}
}

// The reply goes through the same redaction and size limit as every other retained string, and the
// marker still describes the original, so a reader can tell the stored copy was altered.
func TestClaudeStopReplyIsRedactedAndBounded(t *testing.T) {
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	reply := "Use key " + secret + " then " + strings.Repeat("x", asymptoteobserve.DefaultStringLimit)
	logPath, _ := runHookProcess(t, claudeStopPayload(reply), "--platform", "claude", "stop")

	messages := assistantMessages(t, logPath)
	if len(messages) != 1 {
		t.Fatalf("got %d agent.message events, want 1", len(messages))
	}
	stored := outputText(t, messages[0])
	if strings.Contains(stored, secret) {
		t.Fatalf("stored reply kept the secret: %q", stored[:80])
	}
	if len(stored) >= len(reply) {
		t.Fatalf("stored reply is %d bytes, want it bounded below the original %d", len(stored), len(reply))
	}
	content, _ := messages[0]["content"].(map[string]interface{})
	if content["truncated"] != true || content["redacted"] != true || content["bytes"] != float64(len(reply)) {
		t.Fatalf("content marker = %#v, want truncated, redacted, and the original size", content)
	}
}

// last_assistant_message is read only where a runtime documents it. Devin CLI, Factory Droid and
// Cursor send Stop payloads through the same command, and a key read where the runtime never wrote
// it would be a guess about what it means there.
func TestLastAssistantMessageIsReadOnlyWhereDocumented(t *testing.T) {
	for _, platform := range []string{"cursor"} {
		t.Run(platform, func(t *testing.T) {
			logPath, _ := runHookProcess(t, claudeStopPayload("not this runtime's field"), "--platform", platform, "stop")
			if messages := assistantMessages(t, logPath); len(messages) != 0 {
				t.Fatalf("%s recorded agent.message from last_assistant_message: %#v", platform, messages)
			}
		})
	}
}

// Kiro puts the agent's final reply on the Stop payload as `assistant_response` and nowhere else.
// It goes through the shared emitter, arrives end to end, and is read only from Kiro's own key.
func TestKiroStopReplyEndToEnd(t *testing.T) {
	payload := map[string]interface{}{
		"hook_event_name":        "stop",
		"session_id":             "k-e2e",
		"cwd":                    "/repo",
		"assistant_response":     "Kiro's reply.",
		"last_assistant_message": "not Kiro's field",
	}
	logPath, stdout := runHookProcess(t, payload, "--platform", kiroPlatform, "stop")
	if len(bytes.TrimSpace(stdout)) != 0 {
		t.Fatalf("kiro stop printed %q, want nothing", stdout)
	}
	messages := assistantMessages(t, logPath)
	if len(messages) != 1 || outputText(t, messages[0]) != "Kiro's reply." {
		t.Fatalf("kiro agent.message = %#v, want one carrying assistant_response", messages)
	}
	if got := leaf(messages[0], "session", "id"); got != "k-e2e" {
		t.Fatalf("session.id = %q, want k-e2e", got)
	}
	if got := leaf(messages[0], "harness", "name"); got != "kiro" {
		t.Fatalf("harness.name = %q, want kiro", got)
	}
}

// endpointEventsIfAny is endpointEvents for a log a hook may not have created at all.
func endpointEventsIfAny(t *testing.T, logPath string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) || (err == nil && len(bytes.TrimSpace(data)) == 0) {
		return nil
	}
	return endpointEvents(t, logPath)
}
