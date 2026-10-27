package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

// beaconCursorHook is the command `beacon endpoint install` writes into ~/.cursor/hooks.json.
func beaconCursorHook(command string) string {
	return "'/Users/dev/.beacon/endpoint/hooks/beacon-hooks' --platform cursor --log '/var/log/beacon-agent/runtime.jsonl' --config '/Library/Application Support/Beacon/Endpoint/config.json' " + command
}

// cursorPreToolMatcher is the matcher `beacon endpoint install` gives Beacon's Cursor pre-tool hook.
const cursorPreToolMatcher = "Write|Edit|MultiEdit|Shell|MCP"

// writeCursorHooks installs a user-level Cursor hooks file holding these commands, by event.
func writeCursorHooks(t *testing.T, hooks map[string][]string) {
	t.Helper()
	entries := map[string][]map[string]string{}
	for event, commands := range hooks {
		for _, command := range commands {
			entries[event] = append(entries[event], map[string]string{"command": command})
		}
	}
	writeCursorHookEntries(t, entries)
}

// writeCursorHookEntries installs a user-level Cursor hooks file holding these entries, by event.
func writeCursorHookEntries(t *testing.T, entries map[string][]map[string]string) {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{"version": 1, "hooks": entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	original := cursorUserHooksPath
	cursorUserHooksPath = func() string { return path }
	t.Cleanup(func() { cursorUserHooksPath = original })
}

// setupCursorCompatRun prepares one hook run: the platform its entry declares, the subcommand it
// runs, and an empty runtime log.
func setupCursorCompatRun(t *testing.T, declared, command string) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = declared
	hookCommand = command
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Cleanup(func() {
		hookCommand = ""
		duplicateCursorRun = false
		logging.SuppressEndpointEvents(false)
	})
	return logPath
}

// countEndpointEvents is the number of events in the runtime log, zero when nothing created it.
func countEndpointEvents(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func cursorPromptPayload() map[string]interface{} {
	return map[string]interface{}{
		"conversation_id": "conv-compat",
		"generation_id":   "gen-1",
		"hook_event_name": "beforeSubmitPrompt",
		"prompt":          "summarize this repository",
		"workspace_roots": []interface{}{"/repo"},
	}
}

func TestClaudeHookRecordsNothingWhenCursorHooksRecordTheEvent(t *testing.T) {
	writeCursorHooks(t, map[string][]string{"beforeSubmitPrompt": {beaconCursorHook("prompt-submit")}})

	logPath := setupCursorCompatRun(t, "cursor", "prompt-submit")
	native := runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	if n := countEndpointEvents(t, logPath); n != 1 {
		t.Fatalf("Cursor's own hook wrote %d events, want 1", n)
	}

	logPath = setupCursorCompatRun(t, "claude", "prompt-submit")
	compat := runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	if n := countEndpointEvents(t, logPath); n != 0 {
		t.Fatalf("Claude Code hook run by Cursor wrote %d events, want 0", n)
	}
	// The reply is Cursor's, exactly as before: only the recording stepped aside.
	if !reflect.DeepEqual(compat, native) {
		t.Fatalf("Claude Code hook replied %#v, want Cursor's reply %#v", compat, native)
	}
}

func TestClaudeHookRecordsCursorEventsWhenBeaconCursorHooksAreAbsent(t *testing.T) {
	// No Cursor hooks file: the Claude Code hook is the only one recording Cursor, as before.
	logPath := setupCursorCompatRun(t, "claude", "prompt-submit")
	runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "cursor" {
		t.Fatalf("harness = %q, want cursor", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
}

func TestClaudeHookRecordsCommandsBeaconCursorHooksDoNotRun(t *testing.T) {
	// Beacon's Cursor hooks run session-start but nothing that records prompts.
	writeCursorHooks(t, map[string][]string{"sessionStart": {beaconCursorHook("session-start")}})
	logPath := setupCursorCompatRun(t, "claude", "prompt-submit")
	runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	if n := countEndpointEvents(t, logPath); n != 1 {
		t.Fatalf("wrote %d events, want the prompt recorded once", n)
	}
}

func TestOnlyBeaconCursorEntriesCount(t *testing.T) {
	// A person's own Cursor hook that happens to end in the same word is not Beacon's.
	writeCursorHooks(t, map[string][]string{"beforeSubmitPrompt": {"/usr/local/bin/my-audit prompt-submit"}})
	logPath := setupCursorCompatRun(t, "claude", "prompt-submit")
	runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	if n := countEndpointEvents(t, logPath); n != 1 {
		t.Fatalf("wrote %d events, want the prompt recorded once", n)
	}
}

func TestClaudePayloadsStillRecordWithBeaconCursorHooksInstalled(t *testing.T) {
	writeCursorHooks(t, map[string][]string{"beforeSubmitPrompt": {beaconCursorHook("prompt-submit")}})
	logPath := setupCursorCompatRun(t, "claude", "prompt-submit")
	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "claude-session",
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "summarize this repository",
		"cwd":             "/repo",
	})
	event := lastEndpointEvent(t, logPath)
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "claude_code" {
		t.Fatalf("harness = %q, want claude_code", harness)
	}
}

func TestSuppressionDoesNotOutliveTheRun(t *testing.T) {
	writeCursorHooks(t, map[string][]string{"beforeSubmitPrompt": {beaconCursorHook("prompt-submit")}})
	logPath := setupCursorCompatRun(t, "claude", "prompt-submit")
	runHookWithInput(t, runPromptSubmit, cursorPromptPayload())
	// The next run in the same process reads its own payload and decides afresh.
	platformFlag = "claude"
	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "claude-session",
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "next prompt",
		"cwd":             "/repo",
	})
	if n := countEndpointEvents(t, logPath); n != 1 {
		t.Fatalf("wrote %d events, want only the Claude Code prompt", n)
	}
}

func installBeaconCursorPreTool(t *testing.T) {
	t.Helper()
	writeCursorHookEntries(t, map[string][]map[string]string{
		"preToolUse": {{"command": beaconCursorHook("pre-tool"), "matcher": cursorPreToolMatcher}},
	})
}

func cursorToolPayload(tool string, toolInput map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"conversation_id": "conv-compat",
		"hook_event_name": "preToolUse",
		"tool_name":       tool,
		"tool_input":      toolInput,
	}
}

func TestClaudeHookSkipsToolCallsCursorsMatcherSelects(t *testing.T) {
	installBeaconCursorPreTool(t)
	for _, tool := range []string{"Write", "MCP:slack_send_message"} {
		logPath := setupCursorCompatRun(t, "claude", "pre-tool")
		runHookWithInput(t, runPreTool, cursorToolPayload(tool, map[string]interface{}{"file_path": "/repo/notes.md"}))
		if n := countEndpointEvents(t, logPath); n != 0 {
			t.Fatalf("%s: Claude Code hook run by Cursor wrote %d events, want 0", tool, n)
		}
	}
}

func TestClaudeHookStillCoversToolsOnlyItsMatcherSelects(t *testing.T) {
	// Beacon's Cursor pre-tool hook doesn't select Read, so the Claude Code run is the only one
	// that sees the call: it records it and asks the policy provider, as before.
	installBeaconCursorPreTool(t)
	logPath := setupPolicyTest(t, "claude", denyResponse)
	hookCommand = "pre-tool"
	t.Cleanup(func() {
		hookCommand = ""
		duplicateCursorRun = false
		logging.SuppressEndpointEvents(false)
	})
	out := runHookWithInput(t, runPreTool, cursorToolPayload("Read", map[string]interface{}{"file_path": "/repo/.env"}))
	if out["permission"] != "deny" {
		t.Fatalf("Read reply = %#v, want the provider's deny", out)
	}
	assertDenialEvent(t, logPath)
}

func TestCursorMatcherSelects(t *testing.T) {
	cases := []struct {
		matcher, tool string
		want          bool
	}{
		{"", "Read", true},
		{"", "", true},
		{cursorPreToolMatcher, "Write", true},
		{cursorPreToolMatcher, "Shell", true},
		{cursorPreToolMatcher, "MCP:slack_send_message", true},
		{cursorPreToolMatcher, "Read", false},
		{cursorPreToolMatcher, "Grep", false},
		{cursorPreToolMatcher, "", false},
		{"(", "Write", false},
	}
	for _, tc := range cases {
		if got := cursorMatcherSelects(tc.matcher, tc.tool); got != tc.want {
			t.Errorf("cursorMatcherSelects(%q, %q) = %v, want %v", tc.matcher, tc.tool, got, tc.want)
		}
	}
}

func TestDuplicateCursorRunDoesNotAskThePolicyProvider(t *testing.T) {
	installBeaconCursorPreTool(t)
	payload := func() map[string]interface{} {
		return cursorToolPayload("Shell", map[string]interface{}{"command": "claude --dangerously-skip-permissions -p go"})
	}
	t.Cleanup(func() {
		hookCommand = ""
		duplicateCursorRun = false
		logging.SuppressEndpointEvents(false)
	})

	// Cursor's own hook asks the provider and records the deny.
	logPath := setupPolicyTest(t, "cursor", denyResponse)
	hookCommand = "pre-tool"
	if out := runHookWithInput(t, runPreTool, payload()); out["permission"] != "deny" {
		t.Fatalf("Cursor's own hook replied %#v, want permission=deny", out)
	}
	assertDenialEvent(t, logPath)

	// The Claude Code copy of the same call neither decides nor records.
	logPath = setupPolicyTest(t, "claude", denyResponse)
	hookCommand = "pre-tool"
	if out := runHookWithInput(t, runPreTool, payload()); out["permission"] == "deny" {
		t.Fatalf("duplicate run denied the call: %#v", out)
	}
	if n := countEndpointEvents(t, logPath); n != 0 {
		t.Fatalf("duplicate run wrote %d events, want 0", n)
	}
}

func TestIsBeaconCursorHookCommand(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		command string
		want    bool
	}{
		{"current form", beaconCursorHook("pre-tool"), "pre-tool", true},
		{"older env-prefix form", "BEACON_ENDPOINT_MODE=1 BEACON_ENDPOINT_LOG='/tmp/r.jsonl' '/home/dev/.beacon/endpoint/hooks/beacon-hooks' --platform cursor stop", "stop", true},
		{"windows binary", `'C:\Users\dev\.beacon\endpoint\hooks\beacon-hooks.exe' --platform cursor session-start`, "session-start", true},
		{"equals flag", "/opt/beacon/bin/beacon-hooks --platform=cursor prompt-submit", "prompt-submit", true},
		{"another command", beaconCursorHook("pre-tool"), "post-tool", false},
		{"claude entry", "'/Users/dev/.beacon/endpoint/hooks/beacon-hooks' --platform claude prompt-submit", "prompt-submit", false},
		{"someone else's binary", "/usr/local/bin/audit --platform cursor prompt-submit", "prompt-submit", false},
		{"empty", "", "prompt-submit", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBeaconCursorHookCommand(tc.line, tc.command); got != tc.want {
				t.Fatalf("isBeaconCursorHookCommand(%q, %q) = %v, want %v", tc.line, tc.command, got, tc.want)
			}
		})
	}
}

func TestCursorRecordsThisHookToleratesBadFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	original := cursorUserHooksPath
	cursorUserHooksPath = func() string { return path }
	t.Cleanup(func() { cursorUserHooksPath = original })

	if cursorRecordsThisHook("prompt-submit", nil) {
		t.Fatal("a missing file reported Beacon's Cursor hooks as installed")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cursorRecordsThisHook("prompt-submit", nil) {
		t.Fatal("an unreadable file reported Beacon's Cursor hooks as installed")
	}
	if cursorRecordsThisHook("", nil) {
		t.Fatal("an unknown command matched")
	}
}
