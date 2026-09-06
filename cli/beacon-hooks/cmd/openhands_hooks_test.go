package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// OpenHands hook payloads are the SDK's HookEvent model, and the fixtures below are that model's
// serialized shape rather than an approximation of it: `event_type`, `tool_name`, `tool_input`,
// `tool_response`, `message`, `session_id`, `working_dir`, `metadata`, with tool_input and
// tool_response as pydantic dumps carrying a `kind` discriminator.
//
// They matter more than the usual fixture does, because OpenHands is silent about a hook it does
// not like: a hooks.json that fails validation is dropped with a log line the user never sees, and
// a hook that answers with the wrong shape simply has no effect. Nothing in the runtime tells
// Beacon that a mapping stopped working, so these tests are the only thing that would.

func openHandsTestSetup(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	// Beacon's own workspace is a git repository, and the fixtures below name directories that are
	// not, so branch resolution would otherwise shell out once per event for an answer no
	// assertion reads.
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	return logPath
}

// terminalObservation is a TerminalObservation dump: the command echoed back, an exit code, and
// the output as a list of typed content parts rather than a string.
func terminalObservation(command string, exitCode interface{}, output string) map[string]interface{} {
	return map[string]interface{}{
		"kind":      "TerminalObservation",
		"content":   []interface{}{map[string]interface{}{"type": "text", "text": output}},
		"is_error":  false,
		"command":   command,
		"exit_code": exitCode,
		"timeout":   false,
		"metadata":  map[string]interface{}{"exit_code": exitCode, "working_dir": "/workspace"},
	}
}

// fileEditorObservation is a FileEditorObservation dump. old_content and new_content are the
// file's whole contents on either side of the edit, which is what makes an exact diff possible.
func fileEditorObservation(command, path, oldContent, newContent string, prevExist bool) map[string]interface{} {
	return map[string]interface{}{
		"kind":        "FileEditorObservation",
		"content":     []interface{}{map[string]interface{}{"type": "text", "text": "The file " + path + " has been edited."}},
		"is_error":    false,
		"command":     command,
		"path":        path,
		"prev_exist":  prevExist,
		"old_content": oldContent,
		"new_content": newContent,
	}
}

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

// The session id is spelled exactly as Claude spells it, so the default reader finds it with no
// OpenHands branch. Pinned rather than assumed: this is the field every event's session.id comes
// from, and a refactor of the default branch would take OpenHands' session identity with it.
func TestOpenHandsSessionIDComesFromTheSharedReader(t *testing.T) {
	input := map[string]interface{}{
		"event_type":  "PreToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
	}
	if got := resolveSessionID(input, openHandsPlatform); got != "oh-session-1" {
		t.Fatalf("resolveSessionID = %q, want oh-session-1", got)
	}
	// OpenHands hands hooks no transcript: conversation state lives in its own persistence
	// directory and no payload field points at it. Empty is the honest answer, and reading a
	// Claude-shaped transcript_path key that never arrives would read as an oversight.
	sessionID, transcriptPath := resolveSessionIDWithTranscript(input, openHandsPlatform)
	if sessionID != "oh-session-1" {
		t.Fatalf("resolveSessionIDWithTranscript session = %q, want oh-session-1", sessionID)
	}
	if transcriptPath != "" {
		t.Fatalf("resolveSessionIDWithTranscript transcript = %q, want empty", transcriptPath)
	}
}

// working_dir, not cwd. Without the branch this reads empty, and session.working_directory,
// repository and branch all go missing from every OpenHands event.
func TestOpenHandsWorkingDirectoryComesFromWorkingDir(t *testing.T) {
	input := map[string]interface{}{"working_dir": "/workspace/project"}
	if got := resolveCwd(input, openHandsPlatform); got != "/workspace/project" {
		t.Fatalf("resolveCwd = %q, want /workspace/project", got)
	}
	// A payload that used the Claude spelling still resolves, so a hooks.json shared between the
	// two tools does not lose its workspace.
	if got := resolveCwd(map[string]interface{}{"cwd": "/other"}, openHandsPlatform); got != "/other" {
		t.Fatalf("resolveCwd(cwd) = %q, want /other", got)
	}
}

// HookEvent.working_dir is optional and a HookManager built without one leaves it null, while the
// executor always exports OPENHANDS_PROJECT_DIR from its own working directory. The variable is
// the fallback for exactly that case.
func TestOpenHandsWorkingDirectoryFallsBackToTheProjectDirEnv(t *testing.T) {
	t.Setenv("OPENHANDS_PROJECT_DIR", "/workspace/from-env")
	for _, input := range []map[string]interface{}{
		{},
		{"working_dir": nil},
		{"working_dir": ""},
	} {
		if got := resolveCwd(input, openHandsPlatform); got != "/workspace/from-env" {
			t.Fatalf("resolveCwd(%#v) = %q, want the OPENHANDS_PROJECT_DIR fallback", input, got)
		}
	}
	// The payload is the more specific statement and wins when both are present.
	t.Setenv("OPENHANDS_PROJECT_DIR", "/workspace/from-env")
	if got := resolveCwd(map[string]interface{}{"working_dir": "/workspace/from-payload"}, openHandsPlatform); got != "/workspace/from-payload" {
		t.Fatalf("resolveCwd = %q, want the payload to beat the environment", got)
	}
}

// OpenHands gets its own state directory. Sharing Claude's would interleave two runtimes' hook
// logs and let a stale-session sweep for one walk the other's sessions.
func TestOpenHandsHasItsOwnStateDirectory(t *testing.T) {
	setupHookConfigDirs(t)
	stateDir := hookconfig.GetStateDir(openHandsPlatform)
	if stateDir != hookconfig.OpenHandsDir {
		t.Fatalf("GetStateDir(openhands) = %q, want OpenHandsDir %q", stateDir, hookconfig.OpenHandsDir)
	}
	if stateDir == hookconfig.ClaudeDir {
		t.Fatalf("GetStateDir(openhands) = %q, want a directory distinct from Claude's", stateDir)
	}
	if got, want := hookconfig.GetLogFile(openHandsPlatform), filepath.Join(hookconfig.OpenHandsDir, "hooks.log"); got != want {
		t.Fatalf("GetLogFile(openhands) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Prompt
// ---------------------------------------------------------------------------

func TestOpenHandsPromptComesFromMessage(t *testing.T) {
	logPath := openHandsTestSetup(t)

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"event_type":  "UserPromptSubmit",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"message":     "add a health endpoint",
		"metadata":    map[string]interface{}{},
	})
	if len(out) != 0 {
		t.Fatalf("prompt-submit response = %#v, want an empty object", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", got)
	}
	if got := leaf(event, "prompt", "text"); got != "add a health endpoint" {
		t.Fatalf("prompt.text = %q, want the message text", got)
	}
	if got := leaf(event, "harness", "name"); got != "openhands" {
		t.Fatalf("harness.name = %q, want the canonical openhands", got)
	}
	if got := leaf(event, "harness", "collection_method"); got != "hook" {
		t.Fatalf("harness.collection_method = %q, want hook", got)
	}
	if got := leaf(event, "session", "working_directory"); got != "/workspace/project" {
		t.Fatalf("session.working_directory = %q, want /workspace/project", got)
	}
	// The retention marker is what records that the text stored here is the whole prompt rather
	// than a redacted or truncated copy of it.
	if _, ok := event["content"].(map[string]interface{}); !ok {
		t.Fatalf("event carried no content marker for the retained prompt: %#v", event["content"])
	}
}

// `message` is read for OpenHands and must not be added to the shared key list. It is an ordinary
// key that carries something other than a user prompt in other runtimes' payloads, so widening
// the shared list would put a status line or a tool result into prompt.text elsewhere.
func TestOpenHandsMessageKeyIsNotReadForOtherRuntimes(t *testing.T) {
	logPath := openHandsTestSetup(t)
	platformFlag = "claude"

	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "claude-session",
		"cwd":             "/repo",
		"message":         "not a prompt on this runtime",
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "prompt", "text"); got != "" {
		t.Fatalf("prompt.text = %q, want empty -- `message` must stay an OpenHands-only reader", got)
	}
}

// ---------------------------------------------------------------------------
// Pre-tool
// ---------------------------------------------------------------------------

// OpenHands exposes no approval hook: PreToolUse announces a call the agent is about to make, not
// a question anybody was asked. Recording an approval here would put an operator decision in the
// log that no operator made, which is the call Cline, Pi and fx already settled the same way.
func TestOpenHandsPreToolObservesWithoutSynthesizingAnApproval(t *testing.T) {
	logPath := openHandsTestSetup(t)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"event_type":  "PreToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "terminal",
		"tool_input": map[string]interface{}{
			"kind": "TerminalAction", "command": "rm -rf /tmp/data", "is_input": false, "reset": false,
		},
		"metadata": map[string]interface{}{},
	})
	// Anything but an empty object is a claim. OpenHands reads `decision`, `reason`,
	// `additionalContext` and `continue` from a hook's stdout, and shows the raw string on the
	// HookExecutionEvent in the conversation, so a speculative "allow" would be visible to the
	// user as a decision Beacon did not make -- and would start taking effect if OpenHands ever
	// reads that key.
	if len(out) != 0 {
		t.Fatalf("pre-tool response = %#v, want an empty object", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", got)
	}
	if got := leaf(event, "event", "category"); got == "approval" {
		t.Fatalf("event.category = approval; OpenHands has no approval hook to report")
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("event carried an approval block: %#v", event["approval"])
	}
	if got := leaf(event, "command", "command"); got != "rm -rf /tmp/data" {
		t.Fatalf("command.command = %q, want the command the tool is about to run", got)
	}
	if got := leaf(event, "event", "fidelity"); got != "observed" {
		t.Fatalf("event.fidelity = %q, want observed -- OpenHands named this tool call", got)
	}
}

// file_editor's `command` argument (view, str_replace, create) is the editor operation, not a
// shell command. Promoting it into command.command would store an editor operation as shell
// execution and let the policy seam upgrade tool.invoked to command.executed.
func TestOpenHandsPreToolFileEditorDoesNotCarryAShellCommandBlock(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPreTool, map[string]interface{}{
		"event_type":  "PreToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "view", "path": "/workspace/project/main.go",
		},
		"metadata": map[string]interface{}{},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", got)
	}
	if got := leaf(event, "command", "command"); got != "" {
		t.Fatalf("command.command = %q, want empty -- the editor operation is not a shell command", got)
	}
}

// The policy seam must not reclassify a file_editor call as command.executed. The upgrade from
// tool.invoked to command.executed fires when fields carry a command block, so the fix is to not
// set that block for editor tools whose `command` argument names an operation, not a shell command.
func TestOpenHandsPolicyCandidateDoesNotUpgradeFileEditorToCommand(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = openHandsPlatform

	input := map[string]interface{}{
		"event_type":  "PreToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "view", "path": "/workspace/project/main.go",
		},
	}
	candidate := newPolicyCandidate(input, "oh-session-1")
	if candidate.action == "command.executed" {
		t.Fatalf("policy candidate action = command.executed; file_editor view is not a shell command")
	}
	if _, ok := candidate.fields["command"]; ok {
		t.Fatalf("policy candidate carried a command block: %#v", candidate.fields["command"])
	}
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

func TestOpenHandsTerminalRecordsCommandExitCodeAndOutput(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "terminal",
		"tool_input": map[string]interface{}{
			"kind": "TerminalAction", "command": "go test ./...", "is_input": false, "reset": false,
		},
		"tool_response": terminalObservation("go test ./...", float64(0), "ok\tbeacon\t0.4s\n"),
		"metadata":      map[string]interface{}{},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	if got := leaf(event, "event", "category"); got != "command" {
		t.Fatalf("event.category = %q, want command", got)
	}
	if got := leaf(event, "command", "command"); got != "go test ./..." {
		t.Fatalf("command.command = %q, want the command that ran", got)
	}
	command, _ := event["command"].(map[string]interface{})
	if got, ok := command["exit_code"].(float64); !ok || got != 0 {
		t.Fatalf("command.exit_code = %#v, want 0", command["exit_code"])
	}
	if got := leaf(event, "command", "output"); !strings.Contains(got, "ok\tbeacon") {
		t.Fatalf("command.output = %q, want the observation's text content", got)
	}
	if _, ok := event["content"].(map[string]interface{}); !ok {
		t.Fatalf("event carried no content marker for the retained output: %#v", event["content"])
	}
}

// A non-zero exit is an ordinary outcome, not a tool failure. OpenHands leaves is_error false for
// a command that ran and returned non-zero, and reading the code as a failure would replace every
// failing test run and every grep that matched nothing with tool.failed at high severity.
func TestOpenHandsNonZeroExitStaysACommandExecution(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":    "PostToolUse",
		"session_id":    "oh-session-1",
		"working_dir":   "/workspace/project",
		"tool_name":     "terminal",
		"tool_input":    map[string]interface{}{"kind": "TerminalAction", "command": "go test ./..."},
		"tool_response": terminalObservation("go test ./...", float64(1), "FAIL\tbeacon\n"),
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed for a non-zero exit", got)
	}
	command, _ := event["command"].(map[string]interface{})
	if got, ok := command["exit_code"].(float64); !ok || got != 1 {
		t.Fatalf("command.exit_code = %#v, want 1", command["exit_code"])
	}
}

// exit_code is null while a command is still running or was interrupted. Writing a zero for an
// absent code would report every unfinished command as a clean success, so the field is omitted
// instead.
func TestOpenHandsAbsentExitCodeIsNotRecordedAsZero(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":    "PostToolUse",
		"session_id":    "oh-session-1",
		"working_dir":   "/workspace/project",
		"tool_name":     "terminal",
		"tool_input":    map[string]interface{}{"kind": "TerminalAction", "command": "npm run dev"},
		"tool_response": terminalObservation("npm run dev", nil, "listening on :3000\n"),
	})

	event := lastEndpointEvent(t, logPath)
	command, _ := event["command"].(map[string]interface{})
	if _, present := command["exit_code"]; present {
		t.Fatalf("command.exit_code = %#v, want the field omitted for a null code", command["exit_code"])
	}
	if got := leaf(event, "command", "output"); got == "" {
		t.Fatalf("command.output was empty; the output survives an absent exit code")
	}
}

// is_error is the tool itself failing -- a bad argument, an unrecoverable terminal session -- and
// it is the only failure signal OpenHands sends: no failure event name, no top-level `error`.
func TestOpenHandsToolErrorIsRecordedAsAFailure(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "terminal",
		"tool_input":  map[string]interface{}{"kind": "TerminalAction", "command": "["},
		"tool_response": map[string]interface{}{
			"kind":      "TerminalObservation",
			"content":   []interface{}{map[string]interface{}{"type": "text", "text": "tool-argument error"}},
			"is_error":  true,
			"command":   "[",
			"exit_code": nil,
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
	if got := event["severity"]; got != "high" {
		t.Fatalf("severity = %v, want high", got)
	}
}

// ---------------------------------------------------------------------------
// file_editor
// ---------------------------------------------------------------------------

// file_editor multiplexes five operations onto one tool name, selected by the action's `command`.
// A classifier that only sees the name is wrong on one of view/str_replace whichever way it
// answers, which is misclassification rather than absence: the event is there, the action is
// wrong, and nothing looks broken.
func TestOpenHandsFileEditorViewIsARead(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "view", "path": "/workspace/project/main.go",
		},
		"tool_response": map[string]interface{}{
			"kind":       "FileEditorObservation",
			"content":    []interface{}{map[string]interface{}{"type": "text", "text": "package main"}},
			"is_error":   false,
			"command":    "view",
			"path":       "/workspace/project/main.go",
			"prev_exist": true,
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.read" {
		t.Fatalf("event.action = %q, want file.read for command=view", got)
	}
	if got := leaf(event, "file", "operation"); got != "read" {
		t.Fatalf("file.operation = %q, want read", got)
	}
	if got := leaf(event, "file", "path"); got != "/workspace/project/main.go" {
		t.Fatalf("file.path = %q, want the viewed path", got)
	}
	if got := leaf(event, "file", "diff"); got != "" {
		t.Fatalf("file.diff = %q, want no diff for a read", got)
	}
	if got := leaf(event, "command", "command"); got != "" {
		t.Fatalf("command.command = %q, want empty -- the editor operation is not a shell command", got)
	}
}

func TestOpenHandsFileEditorEditRecordsAnExactDiff(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "str_replace",
			"path": "/workspace/project/main.go", "old_str": "old", "new_str": "new",
		},
		"tool_response": fileEditorObservation("str_replace", "/workspace/project/main.go",
			"package main\n\nfunc old() {}\n", "package main\n\nfunc new() {}\n", true),
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	if got := leaf(event, "file", "path"); got != "/workspace/project/main.go" {
		t.Fatalf("file.path = %q, want the edited path", got)
	}
	diff := leaf(event, "file", "diff")
	// The diff comes from the file's content before and after, not from the old_str/new_str the
	// model wrote, so it describes what landed on disk rather than what was requested.
	if !strings.Contains(diff, "-func old() {}") || !strings.Contains(diff, "+func new() {}") {
		t.Fatalf("file.diff = %q, want the before/after content change", diff)
	}
	if leaf(event, "file", "diff_hash") == "" {
		t.Fatalf("file.diff_hash was empty; the hash describes the original diff")
	}
	if got := leaf(event, "session", "id"); got != "oh-session-1" {
		t.Fatalf("session.id = %q, want oh-session-1", got)
	}
	if got := leaf(event, "harness", "name"); got != "openhands" {
		t.Fatalf("harness.name = %q, want openhands", got)
	}
	if got := leaf(event, "command", "command"); got != "" {
		t.Fatalf("command.command = %q, want empty -- str_replace is an editor operation, not a shell command", got)
	}
}

func TestOpenHandsFileEditorCreateRecordsANewFileDiff(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "create",
			"path": "/workspace/project/new.py", "file_text": "print('hi')\n",
		},
		"tool_response": fileEditorObservation("create", "/workspace/project/new.py", "", "print('hi')\n", false),
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	diff := leaf(event, "file", "diff")
	if !strings.Contains(diff, "@@ -0,0 +1,") || !strings.Contains(diff, "+print('hi')") {
		t.Fatalf("file.diff = %q, want a new-file hunk", diff)
	}
}

// undo_edit reports old_content and new_content like any other edit, so it takes the same path.
func TestOpenHandsFileEditorUndoIsRecordedAsAnEdit(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input":  map[string]interface{}{"kind": "FileEditorAction", "command": "undo_edit", "path": "/workspace/project/main.go"},
		"tool_response": fileEditorObservation("undo_edit", "/workspace/project/main.go",
			"func new() {}\n", "func old() {}\n", true),
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified for undo_edit", got)
	}
	if diff := leaf(event, "file", "diff"); !strings.Contains(diff, "+func old() {}") {
		t.Fatalf("file.diff = %q, want the restored content", diff)
	}
}

// An unchanged file is not an edit. A header with an empty hunk would record that a file changed
// when it did not, so the payload falls through to the observing path instead: the tool call is
// still recorded, with the path, and without a diff that claims something happened.
func TestOpenHandsUnchangedContentRecordsNoDiff(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input":  map[string]interface{}{"kind": "FileEditorAction", "command": "undo_edit", "path": "/workspace/project/main.go"},
		"tool_response": fileEditorObservation("undo_edit", "/workspace/project/main.go",
			"same\n", "same\n", true),
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified -- the call still happened", got)
	}
	if got := leaf(event, "file", "diff"); got != "" {
		t.Fatalf("file.diff = %q, want no diff when the content did not change", got)
	}
}

// A failed edit carries the same tool name and the same arguments as a successful one, so without
// the guard a diff would be built from a write that never landed and the log would assert a file
// changed when it did not.
func TestOpenHandsFailedEditIsNotRecordedAsAnEdit(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "file_editor",
		"tool_input": map[string]interface{}{
			"kind": "FileEditorAction", "command": "str_replace",
			"path": "/workspace/project/main.go", "old_str": "absent", "new_str": "new",
		},
		"tool_response": map[string]interface{}{
			"kind":     "FileEditorObservation",
			"content":  []interface{}{map[string]interface{}{"type": "text", "text": "No replacement performed"}},
			"is_error": true,
			"command":  "str_replace",
			"path":     "/workspace/project/main.go",
		},
	})

	for _, event := range endpointEvents(t, logPath) {
		if got := leaf(event, "event", "action"); got == "file.modified" {
			t.Fatalf("a failed edit was recorded as file.modified: %#v", event)
		}
	}
	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
}

// A file_editor call whose command did not arrive says a tool ran and nothing about what it did to
// the file, which is exactly what is known. Falling through to the generic classifier would let
// the substring rules answer from the tool's name, and the name is what cannot distinguish a view
// from a write on this runtime.
func TestOpenHandsFileEditorWithoutACommandIsNotClassifiedAsAnEdit(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform

	got := actionForTool("PostToolUse", "file_editor",
		map[string]interface{}{"kind": "FileEditorAction", "path": "/workspace/main.go"}, nil)
	if got != "tool.invoked" {
		t.Fatalf("actionForTool(file_editor, no command) = %q, want tool.invoked", got)
	}
}

// planning_file_editor subclasses the same action and observation, so it takes the same commands
// and reports the same fields.
func TestOpenHandsPlanningFileEditorSharesTheEditorTaxonomy(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform

	for command, want := range map[string]string{"view": "file.read", "create": "file.modified", "str_replace": "file.modified"} {
		toolInput := map[string]interface{}{"kind": "PlanningFileEditorAction", "command": command, "path": "/workspace/plan.md"}
		if got := actionForTool("PostToolUse", "planning_file_editor", toolInput, nil); got != want {
			t.Errorf("actionForTool(planning_file_editor, %s) = %q, want %q", command, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// apply_patch
// ---------------------------------------------------------------------------

// One apply_patch call commits a change per file and reports them together. Recording only the
// first would assert that a patch touched one file when it touched several.
func TestOpenHandsApplyPatchRecordsEveryChangedFile(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "apply_patch",
		"tool_input": map[string]interface{}{
			"kind":  "ApplyPatchAction",
			"patch": "*** Begin Patch\n*** Update File: a.py\n*** End Patch",
		},
		"tool_response": map[string]interface{}{
			"kind":     "ApplyPatchObservation",
			"content":  []interface{}{map[string]interface{}{"type": "text", "text": "Applied patch"}},
			"is_error": false,
			"message":  "Applied patch",
			"fuzz":     float64(0),
			"commit": map[string]interface{}{
				"changes": map[string]interface{}{
					"/workspace/project/a.py": map[string]interface{}{
						"type": "update", "old_content": "x = 1\n", "new_content": "x = 2\n",
					},
					"/workspace/project/b.py": map[string]interface{}{
						"type": "add", "new_content": "y = 3\n",
					},
				},
			},
		},
	})

	events := endpointEvents(t, logPath)
	paths := map[string]string{}
	for _, event := range events {
		if leaf(event, "event", "action") != "file.modified" {
			continue
		}
		paths[leaf(event, "file", "path")] = leaf(event, "file", "diff")
	}
	if len(paths) != 2 {
		t.Fatalf("recorded %d file.modified events, want one per changed file: %#v", len(paths), paths)
	}
	if diff := paths["/workspace/project/a.py"]; !strings.Contains(diff, "-x = 1") || !strings.Contains(diff, "+x = 2") {
		t.Fatalf("a.py diff = %q, want the update", diff)
	}
	if diff := paths["/workspace/project/b.py"]; !strings.Contains(diff, "@@ -0,0 +1,") || !strings.Contains(diff, "+y = 3") {
		t.Fatalf("b.py diff = %q, want a new-file hunk", diff)
	}
}

// A rename keys the change by the source path and names the destination under move_path. The edit
// is recorded against the destination, because that is the file that exists afterwards and the one
// an investigator would look for.
func TestOpenHandsApplyPatchRenameIsRecordedAgainstTheDestination(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "apply_patch",
		"tool_input":  map[string]interface{}{"kind": "ApplyPatchAction", "patch": "*** Begin Patch\n*** End Patch"},
		"tool_response": map[string]interface{}{
			"kind":     "ApplyPatchObservation",
			"is_error": false,
			"commit": map[string]interface{}{
				"changes": map[string]interface{}{
					"/workspace/project/old.py": map[string]interface{}{
						"type": "update", "move_path": "/workspace/project/new.py",
						"old_content": "x = 1\n", "new_content": "x = 2\n",
					},
				},
			},
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "file", "path"); got != "/workspace/project/new.py" {
		t.Fatalf("file.path = %q, want the destination of the rename", got)
	}
}

func TestOpenHandsFailedApplyPatchIsNotRecordedAsAnEdit(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "apply_patch",
		"tool_input":  map[string]interface{}{"kind": "ApplyPatchAction", "patch": "*** Begin Patch\nbroken"},
		"tool_response": map[string]interface{}{
			"kind":     "ApplyPatchObservation",
			"content":  []interface{}{map[string]interface{}{"type": "text", "text": "Invalid patch"}},
			"is_error": true,
		},
	})

	for _, event := range endpointEvents(t, logPath) {
		if got := leaf(event, "event", "action"); got == "file.modified" {
			t.Fatalf("a failed patch was recorded as file.modified: %#v", event)
		}
	}
	if got := leaf(lastEndpointEvent(t, logPath), "event", "action"); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
}

// ---------------------------------------------------------------------------
// Gemini-compatible tool set
// ---------------------------------------------------------------------------

// write_file and edit are not in the default preset, but a hook installed once receives payloads
// from whatever tools the agent was configured with. Both report old_content and new_content, so
// they take the same diff path file_editor does.
func TestOpenHandsGeminiEditToolsRecordDiffs(t *testing.T) {
	for _, tc := range []struct {
		toolName string
		kind     string
	}{
		{"write_file", "WriteFileObservation"},
		{"edit", "EditObservation"},
	} {
		t.Run(tc.toolName, func(t *testing.T) {
			logPath := openHandsTestSetup(t)

			runHookWithInput(t, runPostTool, map[string]interface{}{
				"event_type":  "PostToolUse",
				"session_id":  "oh-session-1",
				"working_dir": "/workspace/project",
				"tool_name":   tc.toolName,
				"tool_input":  map[string]interface{}{"file_path": "/workspace/project/app.ts", "content": "b\n"},
				"tool_response": map[string]interface{}{
					"kind":        tc.kind,
					"is_error":    false,
					"file_path":   "/workspace/project/app.ts",
					"is_new_file": false,
					"old_content": "a\n",
					"new_content": "b\n",
				},
			})

			event := lastEndpointEvent(t, logPath)
			if got := leaf(event, "event", "action"); got != "file.modified" {
				t.Fatalf("event.action = %q, want file.modified", got)
			}
			if got := leaf(event, "file", "path"); got != "/workspace/project/app.ts" {
				t.Fatalf("file.path = %q, want the written path", got)
			}
			if diff := leaf(event, "file", "diff"); !strings.Contains(diff, "-a") || !strings.Contains(diff, "+b") {
				t.Fatalf("file.diff = %q, want the content change", diff)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Taxonomy
// ---------------------------------------------------------------------------

// The read-side built-ins. `glob` and `grep` take a `path` that is a directory to search rather
// than a file that was read; recording it under file.path with operation "read" is the same shape
// Qwen Code's equivalents produce and the honest one -- a search did happen, and that is what it
// touched.
func TestOpenHandsReadToolsAreClassifiedAsFileReads(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform

	for _, toolName := range []string{"read_file", "list_directory", "glob", "grep"} {
		t.Run(toolName, func(t *testing.T) {
			if got := actionForTool("PostToolUse", toolName, nil, nil); got != "file.read" {
				t.Errorf("actionForTool(%q) = %q, want file.read", toolName, got)
			}
			if got := openHandsFileOperation(toolName, nil, nil); got != "read" {
				t.Errorf("openHandsFileOperation(%q) = %q, want read", toolName, got)
			}
		})
	}
}

// The tools with no filesystem or shell meaning stay on the shared path, where tool.invoked is
// already the right answer. Returning "" from the OpenHands classifier rather than a default is
// what keeps them there.
func TestOpenHandsNonFilesystemToolsFallThroughToTheGenericClassifier(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform

	for _, toolName := range []string{"think", "finish", "task_tracker", "browser_navigate", "browser_click", "invoke_skill", "ask_oracle"} {
		t.Run(toolName, func(t *testing.T) {
			if got := openHandsToolAction(toolName, nil, nil); got != "" {
				t.Errorf("openHandsToolAction(%q) = %q, want \"\" so the shared classifier decides", toolName, got)
			}
			if got := actionForTool("PostToolUse", toolName, nil, nil); got != "tool.invoked" {
				t.Errorf("actionForTool(%q) = %q, want tool.invoked", toolName, got)
			}
		})
	}
}

// The OpenHands taxonomy must not leak into other runtimes. `file_editor`, `glob` and `grep` are
// plausible tool names elsewhere, and claiming them for every platform would rewrite another
// runtime's recorded actions with no fixture to say what they should become.
func TestOpenHandsTaxonomyIsScopedToTheOpenHandsPlatform(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"

	toolInput := map[string]interface{}{"command": "view", "path": "/repo/main.go"}
	if got := actionForTool("PostToolUse", "file_editor", toolInput, nil); got == "file.read" {
		t.Fatalf("actionForTool(claude, file_editor) = %q; the OpenHands editor taxonomy leaked", got)
	}
	if got := actionForTool("PostToolUse", "glob", nil, nil); got != "tool.invoked" {
		t.Fatalf("actionForTool(claude, glob) = %q, want the claude classification unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

// OpenHands calls an MCP tool by whatever name its server gave it, with no prefix and no mcp_*
// argument, so every generic signal misses it and the call would be recorded as an ordinary
// tool.invoked. The observation is what says so: an MCP call returns MCPToolObservation.
func TestOpenHandsMCPToolCallIsRecognizedFromTheObservation(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":  "PostToolUse",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"tool_name":   "search_issues",
		"tool_input":  map[string]interface{}{"query": "is:open"},
		"tool_response": map[string]interface{}{
			"kind":      "MCPToolObservation",
			"content":   []interface{}{map[string]interface{}{"type": "text", "text": "[Tool 'search_issues' executed.]"}},
			"is_error":  false,
			"tool_name": "search_issues",
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := leaf(event, "event", "action"); got != "mcp.tool_invoked" {
		t.Fatalf("event.action = %q, want mcp.tool_invoked", got)
	}
	if got := leaf(event, "event", "category"); got != "mcp" {
		t.Fatalf("event.category = %q, want mcp", got)
	}
	if got := leaf(event, "mcp", "tool"); got != "search_issues" {
		t.Fatalf("mcp.tool = %q, want the tool the server named", got)
	}
	if got := leaf(event, "mcp", "method", "name"); got != "tools/call" {
		t.Fatalf("mcp.method.name = %q, want tools/call", got)
	}
}

// An MCP tool's name is chosen by whoever wrote the server and can collide with any built-in id,
// so a result that says it came from MCP outranks a name that happens to match.
func TestOpenHandsMCPObservationOutranksACollidingBuiltinName(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = openHandsPlatform

	response := map[string]interface{}{"kind": "MCPToolObservation", "tool_name": "edit"}
	if got := openHandsToolAction("edit", map[string]interface{}{"path": "/x.go"}, response); got != "mcp.tool_invoked" {
		t.Fatalf("openHandsToolAction(edit, MCP result) = %q, want mcp.tool_invoked", got)
	}
}

// Matched exactly rather than by substring: `kind` is a class name from a closed set, not free
// text, so a substring rule would claim any future observation class whose name merely contained
// these letters.
func TestOpenHandsMCPDetectionMatchesTheObservationKindExactly(t *testing.T) {
	for _, kind := range []string{"TerminalObservation", "FileEditorObservation", "MCPToolObservationExtended", "mcptoolobservation", ""} {
		if openHandsIsMCPToolCall(map[string]interface{}{"kind": kind}) {
			t.Errorf("openHandsIsMCPToolCall(kind=%q) = true, want false", kind)
		}
	}
	if !openHandsIsMCPToolCall(map[string]interface{}{"kind": "MCPToolObservation"}) {
		t.Fatal("openHandsIsMCPToolCall(MCPToolObservation) = false, want true")
	}
	if openHandsIsMCPToolCall(nil) {
		t.Fatal("openHandsIsMCPToolCall(nil) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Observation content
// ---------------------------------------------------------------------------

// An observation's content is a list of typed parts. Only text is read: an image part carries a
// base64 data URL, often megabytes of it, and describing it would write that into an event field.
func TestOpenHandsObservationTextReadsOnlyTextParts(t *testing.T) {
	got := openHandsObservationText(map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": "first\n"},
			map[string]interface{}{"type": "image", "image_urls": []interface{}{"data:image/png;base64,AAAA"}},
			map[string]interface{}{"type": "text", "text": "second\n"},
		},
	})
	if got != "first\nsecond\n" {
		t.Fatalf("openHandsObservationText = %q, want the text parts joined in order", got)
	}
	for _, response := range []map[string]interface{}{nil, {}, {"content": "not a list"}, {"content": []interface{}{"not a part"}}} {
		if got := openHandsObservationText(response); got != "" {
			t.Errorf("openHandsObservationText(%#v) = %q, want empty", response, got)
		}
	}
}

// is_error arrives as a JSON bool; the string spelling is tolerated so a payload that reached this
// command by some other route is still read correctly. Nothing else counts as a failure.
func TestOpenHandsToolFailedReadsOnlyIsError(t *testing.T) {
	for _, response := range []map[string]interface{}{
		{"is_error": true},
		{"is_error": "true"},
		{"is_error": "TRUE"},
	} {
		if !openHandsToolFailed(response) {
			t.Errorf("openHandsToolFailed(%#v) = false, want true", response)
		}
	}
	for _, response := range []map[string]interface{}{
		nil,
		{},
		{"is_error": false},
		{"is_error": "false"},
		{"is_error": nil},
		{"exit_code": float64(1)},
	} {
		if openHandsToolFailed(response) {
			t.Errorf("openHandsToolFailed(%#v) = true, want false", response)
		}
	}
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

func TestOpenHandsSessionLifecycleEvents(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runSessionStart, map[string]interface{}{
		"event_type":  "SessionStart",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"metadata":    map[string]interface{}{},
	})
	if got := leaf(lastEndpointEvent(t, logPath), "event", "action"); got != "session.started" {
		t.Fatalf("event.action = %q, want session.started", got)
	}

	// Stop fires when the agent tries to finish. Its metadata carries reason="agent_finished", a
	// constant the SDK passes at its single call site, which is why the payload is not retained.
	//
	// The event is built through emitHookEvent with the arguments runStop passes it, rather than by
	// running the command: runStop ends the session-bearing path with outputJSONAndExit, and os.Exit
	// in an in-process test aborts the run. The mapping this asserts -- that an OpenHands Stop
	// payload yields a session-bearing tool.completed with the workspace attached -- is exactly what
	// the command performs. Same workaround, same reason, as the Qwen Stop test.
	stopInput := map[string]interface{}{
		"event_type":  "Stop",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"metadata":    map[string]interface{}{"reason": "agent_finished"},
	}
	stopSession, _ := resolveSessionIDWithTranscript(stopInput, platformFlag)
	if stopSession != "oh-session-1" {
		t.Fatalf("stop session id = %q; the mapping under test would not run", stopSession)
	}
	emitHookEvent(newHookLogger("stop", platformFlag, stopSession), "tool.completed", "tool", "info",
		"Agent response completed", stopInput, sessionFields(stopSession, stopInput))
	stopEvent := lastEndpointEvent(t, logPath)
	if got := leaf(stopEvent, "event", "action"); got != "tool.completed" {
		t.Fatalf("event.action = %q, want tool.completed", got)
	}
	if got := leaf(stopEvent, "session", "working_directory"); got != "/workspace/project" {
		t.Fatalf("session.working_directory = %q, want the workspace on the stop event", got)
	}

	runHookWithInput(t, runSessionEnd, map[string]interface{}{
		"event_type":  "SessionEnd",
		"session_id":  "oh-session-1",
		"working_dir": "/workspace/project",
		"metadata":    map[string]interface{}{},
	})
	last := lastEndpointEvent(t, logPath)
	if got := leaf(last, "event", "action"); got != "session.ended" {
		t.Fatalf("event.action = %q, want session.ended", got)
	}
	if got := leaf(last, "session", "working_directory"); got != "/workspace/project" {
		t.Fatalf("session.working_directory = %q, want the workspace on the closing event too", got)
	}
}

// metadata is empty on five of the six events and carries a constant on Stop, so retaining the
// payload the way the Qwen and Muse mappings do would cost the whole tool_input and tool_response
// on every event -- file contents included -- to preserve a value that never varies.
func TestOpenHandsDoesNotRetainTheRawPayload(t *testing.T) {
	logPath := openHandsTestSetup(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"event_type":    "PostToolUse",
		"session_id":    "oh-session-1",
		"working_dir":   "/workspace/project",
		"tool_name":     "terminal",
		"tool_input":    map[string]interface{}{"kind": "TerminalAction", "command": "ls"},
		"tool_response": terminalObservation("ls", float64(0), "main.go\n"),
	})

	event := lastEndpointEvent(t, logPath)
	if raw, ok := event["raw"].(map[string]interface{}); ok {
		if _, present := raw[openHandsPlatform]; present {
			t.Fatalf("event carried raw.openhands: %#v", raw[openHandsPlatform])
		}
	}
}

// ---------------------------------------------------------------------------
// Policy seam
// ---------------------------------------------------------------------------

// The seam is off by default and fails open, but when a provider does deny, the response has to be
// the shape OpenHands reads or the deny is silently dropped and the tool runs anyway.
//
// The reason is worth sending alongside the decision: OpenHands surfaces it in the conversation as
// the explanation for the block and hands it to the agent as the tool's failure, so without it the
// operator and the model both see a call refused with no account of why.
func TestOpenHandsPolicyDenyCarriesADecisionAndAReason(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = openHandsPlatform

	for _, phase := range []policycontract.Phase{policycontract.PhasePreTool, policycontract.PhasePermissionRequest} {
		deny := policyDenyResponse("rm -rf is blocked", phase)
		if deny == nil {
			t.Fatalf("policyDenyResponse(%v) = nil; OpenHands has a confirmed deny shape", phase)
		}
		if got, _ := deny["decision"].(string); got != "deny" {
			t.Fatalf("decision = %q, want deny", got)
		}
		if got, _ := deny["reason"].(string); got != "rm -rf is blocked" {
			t.Fatalf("reason = %q, want the provider's reason", got)
		}
	}
}
