package cmd

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/policy"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// Autopilot's payloads are Claude Code's shape by design, so most of what these tests check is that
// the Claude Code readers apply to them unchanged. The fixtures are captured from Autopilot 2.0.0,
// and those under 2.2.0/ from Autopilot 2.2.0 (see testdata/autopilot/README.md); a test here
// failing after a fixture is refreshed means the runtime changed what it sends, not that the
// fixture is wrong.

// runAutopilotHook feeds one captured Autopilot payload to a subcommand and returns the hook's
// stdout and every event it wrote. transcript_path is pointed at the captured transcript so the
// prompt-id reader sees the file the runtime wrote for the same session.
func runAutopilotHook(t *testing.T, run func(cmd *cobra.Command, args []string), fixture string) (map[string]interface{}, []map[string]interface{}) {
	t.Helper()
	logPath := prepareAutopilotHook(t)
	payload := readHookFixture(t, "autopilot", fixture)
	return runAutopilotPayload(t, run, fixture, payload, logPath)
}

// prepareAutopilotHook points the hook at a fresh endpoint log under the autopilot platform and
// returns the log's path.
func prepareAutopilotHook(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = autopilotPlatform
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	return logPath
}

// runAutopilotPayload runs an already loaded (and possibly edited) fixture. The transcript is the
// one captured beside the fixture, so a fixture under testdata/autopilot/2.2.0 reads that session's.
func runAutopilotPayload(t *testing.T, run func(cmd *cobra.Command, args []string), fixture string, payload map[string]interface{}, logPath string) (map[string]interface{}, []map[string]interface{}) {
	t.Helper()
	if _, ok := payload["transcript_path"]; ok {
		payload["transcript_path"] = filepath.Join("..", "testdata", "autopilot", filepath.Dir(fixture), "transcript.jsonl")
	}
	out := runHookWithInput(t, run, payload)
	return out, endpointEventsIfAny(t, logPath)
}

func onlyAutopilotEvent(t *testing.T, events []map[string]interface{}) map[string]interface{} {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1: %#v", len(events), events)
	}
	return events[0]
}

func autopilotAction(event map[string]interface{}) string {
	action, _ := event["event"].(map[string]interface{})["action"].(string)
	return action
}

func autopilotField(event map[string]interface{}, path ...string) interface{} {
	var current interface{} = event
	for _, key := range path {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

const (
	autopilotFixtureSession = "2026-10-21T05-24-15-760Z_scenario-fixtures-exercise-the-tools"
	autopilotFixturePrompt  = "e5c178cc-98d7-4974-9dd8-71fa80106365"
)

func TestAutopilotHasItsOwnStateDirectory(t *testing.T) {
	setupHookConfigDirs(t)
	stateDir := hookconfig.GetStateDir(autopilotPlatform)
	if stateDir != hookconfig.AutopilotDir {
		t.Fatalf("GetStateDir(autopilot) = %q, want AutopilotDir %q", stateDir, hookconfig.AutopilotDir)
	}
	if stateDir == hookconfig.ClaudeDir {
		t.Fatalf("GetStateDir(autopilot) = %q; sharing Claude Code's directory would mix two runtimes' session state", stateDir)
	}
}

// The reason Autopilot has a platform of its own rather than riding --platform claude: its sessions
// must be filed under its own harness name, or a query grouping by harness.name counts them as
// Claude Code activity.
func TestAutopilotEventsAreFiledUnderAutopilot(t *testing.T) {
	_, events := runAutopilotHook(t, runSessionStart, "session_start.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "session.started" {
		t.Fatalf("event.action = %q, want session.started", got)
	}
	if got := autopilotField(event, "harness", "name"); got != "autopilot" {
		t.Fatalf("harness.name = %v, want autopilot", got)
	}
	if got := autopilotField(event, "harness", "collection_method"); got != "hook" {
		t.Fatalf("harness.collection_method = %v, want hook", got)
	}
	if got := autopilotField(event, "session", "id"); got != autopilotFixtureSession {
		t.Fatalf("session.id = %v, want %s", got, autopilotFixtureSession)
	}
	if got := autopilotField(event, "session", "working_directory"); got != "/home/dev/proj" {
		t.Fatalf("session.working_directory = %v, want the payload's cwd", got)
	}
}

// Pre-tool observes and answers with no decision. Autopilot reads a PreToolUse `allow` as a
// decision, so anything but an empty object would be the hook answering for the user.
func TestAutopilotPreToolObservesWithoutDeciding(t *testing.T) {
	out, events := runAutopilotHook(t, runPreTool, "pre_tool_bash.json")
	if len(out) != 0 {
		t.Fatalf("pre-tool response = %#v, want an empty object carrying no decision", out)
	}
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", got)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("pre-tool event claimed an approval decision: %#v", event["approval"])
	}
	if got := autopilotField(event, "tool", "name"); got != "Bash" {
		t.Fatalf("tool.name = %v, want Bash", got)
	}
	if got := autopilotField(event, "command", "command"); got != "echo hi" {
		t.Fatalf("command.command = %v, want echo hi", got)
	}
	if got := autopilotField(event, "gen_ai", "tool", "call", "id"); got != "call_0" {
		t.Fatalf("gen_ai.tool.call.id = %v, want the payload's tool_use_id", got)
	}
	if got := autopilotField(event, "prompt", "id"); got != autopilotFixturePrompt {
		t.Fatalf("prompt.id = %v, want the newest promptId in Autopilot's transcript", got)
	}
}

func TestAutopilotBashResultIsACommandWithItsOutput(t *testing.T) {
	_, events := runAutopilotHook(t, runPostTool, "post_tool_bash.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	if got := event["severity"]; got != "info" {
		t.Fatalf("severity = %v, want info", got)
	}
	if got := autopilotField(event, "command", "output"); got != "hi\n" {
		t.Fatalf("command.output = %q, want the command's stdout", got)
	}
}

// A non-zero exit arrives as Claude Code's `Exit code N` error line, so it is recorded the way a
// Claude Code failure is: command.executed at high severity, with the exit code and the output.
func TestAutopilotFailedBashKeepsItsExitCode(t *testing.T) {
	_, events := runAutopilotHook(t, runPostTool, "post_tool_bash_failed.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	if got := event["severity"]; got != "high" {
		t.Fatalf("severity = %v, want high", got)
	}
	if got := autopilotField(event, "command", "exit_code"); got != float64(3) {
		t.Fatalf("command.exit_code = %v, want 3", got)
	}
	if got, _ := autopilotField(event, "command", "output").(string); !strings.Contains(got, "oops") {
		t.Fatalf("command.output = %q, want the command's stderr", got)
	}
}

func TestAutopilotFileWritesCarryADiff(t *testing.T) {
	for fixture, want := range map[string]struct{ tool, diffLine string }{
		"post_tool_write.json":     {"Write", "+world"},
		"post_tool_edit.json":      {"Edit", "+hello beacon"},
		"post_tool_multiedit.json": {"MultiEdit", "+WORLD"},
	} {
		t.Run(fixture, func(t *testing.T) {
			_, events := runAutopilotHook(t, runPostTool, fixture)
			event := onlyAutopilotEvent(t, events)
			if got := autopilotAction(event); got != "file.modified" {
				t.Fatalf("event.action = %q, want file.modified", got)
			}
			if got := autopilotField(event, "tool", "name"); got != want.tool {
				t.Fatalf("tool.name = %v, want %s", got, want.tool)
			}
			if got := autopilotField(event, "file", "path"); got != "/home/dev/proj/hello.py" {
				t.Fatalf("file.path = %v, want the absolute file_path Autopilot sends", got)
			}
			if diff, _ := autopilotField(event, "file", "diff").(string); !strings.Contains(diff, want.diffLine) {
				t.Fatalf("file.diff = %q, want it to contain %q", diff, want.diffLine)
			}
			if got := autopilotField(event, "session", "id"); got != autopilotFixtureSession {
				t.Fatalf("session.id = %v, want %s", got, autopilotFixtureSession)
			}
		})
	}
}

func TestAutopilotReadIsAFileRead(t *testing.T) {
	_, events := runAutopilotHook(t, runPostTool, "post_tool_read.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "file.read" {
		t.Fatalf("event.action = %q, want file.read", got)
	}
	if got := autopilotField(event, "file", "path"); got != "/home/dev/proj/hello.py" {
		t.Fatalf("file.path = %v, want /home/dev/proj/hello.py", got)
	}
}

// A write that failed reaches PostToolUse under Autopilot's own lowercase name, so it must be a
// tool.failed and never a file.modified: the log would otherwise assert that a file changed when
// the runtime reported it did not.
func TestAutopilotFailedWriteIsNotAFileChange(t *testing.T) {
	_, events := runAutopilotHook(t, runPostTool, "post_tool_write_failed.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
	if got := event["severity"]; got != "high" {
		t.Fatalf("severity = %v, want high", got)
	}
	if diff := autopilotField(event, "file", "diff"); diff != nil {
		t.Fatalf("failed write carried a diff: %v", diff)
	}
}

// A call the operator (or print mode) refused reaches PostToolUse as a failure.
func TestAutopilotDeniedCallIsAFailure(t *testing.T) {
	_, events := runAutopilotHook(t, runPostTool, "post_tool_permission_denied.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
}

func TestAutopilotPermissionRequestIsObserved(t *testing.T) {
	out, events := runAutopilotHook(t, runPermissionRequest, "permission_request.json")
	if len(out) != 0 {
		t.Fatalf("permission-request response = %#v, want an empty object so Autopilot asks the user", out)
	}
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "approval.requested" {
		t.Fatalf("event.action = %q, want approval.requested", got)
	}
	if got := autopilotField(event, "command", "command"); got != "echo needs-approval" {
		t.Fatalf("command.command = %v, want the call awaiting approval", got)
	}
}

// Stop carries the turn's final reply as last_assistant_message, the field Claude Code uses. Run
// out of process because the stop command ends with os.Exit.
func TestAutopilotStopRecordsTheFinalReply(t *testing.T) {
	payload := readHookFixture(t, "autopilot", "stop.json")
	payload["transcript_path"] = filepath.Join("..", "testdata", "autopilot", "transcript.jsonl")
	logPath, stdout := runHookProcess(t, payload, "--platform", autopilotPlatform, "stop")
	if strings.TrimSpace(string(stdout)) != "{}" {
		t.Fatalf("stop printed %q, want the {} no-op reply", stdout)
	}
	messages := assistantMessages(t, logPath)
	if len(messages) != 1 {
		t.Fatalf("got %d agent.message events, want 1", len(messages))
	}
	if got := outputText(t, messages[0]); got != "FAKE-DONE fixtures after 7 tool results" {
		t.Fatalf("reply = %q, want the Stop payload's last_assistant_message", got)
	}
	for _, check := range []struct {
		path []string
		want string
	}{
		{[]string{"harness", "name"}, "autopilot"},
		{[]string{"session", "id"}, autopilotFixtureSession},
		{[]string{"prompt", "id"}, autopilotFixturePrompt},
	} {
		if got := leaf(messages[0], check.path...); got != check.want {
			t.Errorf("%s = %q, want %q", strings.Join(check.path, "."), got, check.want)
		}
	}
	events := endpointEvents(t, logPath)
	if last := events[len(events)-1]; leaf(last, "event", "action") != "tool.completed" {
		t.Fatalf("last event = %q, want the turn completion after the reply", leaf(last, "event", "action"))
	}
}

func TestAutopilotSessionEnd(t *testing.T) {
	_, events := runAutopilotHook(t, runSessionEnd, "session_end.json")
	event := onlyAutopilotEvent(t, events)
	if got := autopilotAction(event); got != "session.ended" {
		t.Fatalf("event.action = %q, want session.ended", got)
	}
	if got := autopilotField(event, "session", "id"); got != autopilotFixtureSession {
		t.Fatalf("session.id = %v, want %s", got, autopilotFixtureSession)
	}
}

// Autopilot answers both phases of the policy seam with Claude Code's objects: the PreToolUse
// permissionDecision and the PermissionRequest decision.behavior. Sending the wrong one is no deny
// at all -- the runtime finds no decision and the call runs -- so both phases are pinned.
func TestAutopilotPolicyDenyUsesClaudeCodesShapePerPhase(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = autopilotPlatform

	pre := policyDenyResponse("blocked", policycontract.PhasePreTool)
	specific, _ := pre["hookSpecificOutput"].(map[string]interface{})
	if specific["hookEventName"] != "PreToolUse" || specific["permissionDecision"] != "deny" || specific["permissionDecisionReason"] != "blocked" {
		t.Fatalf("pre-tool deny = %#v, want Claude Code's PreToolUse permissionDecision deny", pre)
	}

	perm := policyDenyResponse("blocked", policycontract.PhasePermissionRequest)
	specific, _ = perm["hookSpecificOutput"].(map[string]interface{})
	decision, _ := specific["decision"].(map[string]interface{})
	if specific["hookEventName"] != "PermissionRequest" || decision["behavior"] != "deny" || decision["message"] != "blocked" {
		t.Fatalf("permission deny = %#v, want Claude Code's PermissionRequest decision.behavior deny", perm)
	}
}

func TestPreToolPolicyDenyAutopilot(t *testing.T) {
	logPath := setupPolicyTest(t, autopilotPlatform, denyResponse)
	// The provider is a shell script; on a loaded machine its start-up alone can approach the 2s
	// production timeout, and a timeout fails open, which would read as a missing deny.
	origTimeout := policy.Timeout
	t.Cleanup(func() { policy.Timeout = origTimeout })
	policy.Timeout = 10 * time.Second
	payload := readHookFixture(t, "autopilot", "pre_tool_bash.json")
	delete(payload, "transcript_path")
	out := runHookWithInput(t, runPreTool, payload)
	specific, _ := out["hookSpecificOutput"].(map[string]interface{})
	if specific["permissionDecision"] != "deny" {
		t.Fatalf("autopilot pre-tool response = %#v, want permissionDecision deny", out)
	}
	assertDenialEvent(t, logPath)
	event := lastEndpointEvent(t, logPath)
	if got := autopilotField(event, "harness", "name"); got != "autopilot" {
		t.Fatalf("harness.name = %v, want autopilot", got)
	}
}

const autopilot220Session = "2026-10-21T11-21-32-042Z_scenario-fixtures-exercise-the-tools"

// Autopilot 2.2.0 sends Claude Code's structuredPatch and originalFile on Write, Edit and
// MultiEdit, so the diff is the one the runtime computed against the file: real line numbers,
// context lines, and an overwrite shown with what it replaced. Older releases sent neither, and
// TestAutopilotFileWritesCarryADiff covers the diff rebuilt from the arguments for those.
func TestAutopilot220DiffsComeFromStructuredPatch(t *testing.T) {
	for fixture, want := range map[string]struct{ tool, diff string }{
		// The context line " world" exists only in the patch: the arguments know nothing about it.
		"2.2.0/post_tool_edit.json": {"Edit", "--- a/hello.py\n+++ b/hello.py\n@@ -1,2 +1,2 @@\n-hello\n+hello beacon\n world"},
		// An edit on line 8 of a 12-line file is placed at its line, with three lines of context.
		"2.2.0/post_tool_multiedit_line8.json": {"MultiEdit", "@@ -5,7 +5,7 @@\n l5\n l6\n l7\n-hello there\n+HELLO there\n l9\n l10\n l11"},
		// A Write over an existing file (tool_response.type "update") shows the lines it replaced.
		"2.2.0/post_tool_write_update.json": {"Write", "@@ -1,1 +1,2 @@\n-old line\n+hello\n+world"},
	} {
		t.Run(fixture, func(t *testing.T) {
			_, events := runAutopilotHook(t, runPostTool, fixture)
			event := onlyAutopilotEvent(t, events)
			if got := autopilotAction(event); got != "file.modified" {
				t.Fatalf("event.action = %q, want file.modified", got)
			}
			if got := autopilotField(event, "tool", "name"); got != want.tool {
				t.Fatalf("tool.name = %v, want %s", got, want.tool)
			}
			if diff, _ := autopilotField(event, "file", "diff").(string); !strings.Contains(diff, want.diff) {
				t.Fatalf("file.diff = %q, want it to contain %q", diff, want.diff)
			}
		})
	}
}

// Without structuredPatch (Autopilot before 2.2.0), the same Edit falls back to the diff rebuilt
// from old_string/new_string, which has no context and no line numbers of its own.
func TestAutopilotEditWithoutStructuredPatchFallsBackToTheArguments(t *testing.T) {
	logPath := prepareAutopilotHook(t)
	fixture := "2.2.0/post_tool_edit.json"
	payload := readHookFixture(t, "autopilot", fixture)
	response := payload["tool_response"].(map[string]interface{})
	delete(response, "structuredPatch")
	delete(response, "originalFile")
	_, events := runAutopilotPayload(t, runPostTool, fixture, payload, logPath)
	event := onlyAutopilotEvent(t, events)
	want := "--- a/hello.py\n+++ b/hello.py\n@@ -1,1 +1,1 @@\n-hello\n+hello beacon"
	if diff, _ := autopilotField(event, "file", "diff").(string); diff != want {
		t.Fatalf("file.diff = %q, want %q", diff, want)
	}
}

// Since 2.2.0 Autopilot fires UserPromptSubmit in print mode too, after SessionStart and with the
// session id; 2.0.x fired it only from the interactive session, and the first one had no id.
func TestAutopilot220PromptIsSubmittedWithTheSessionID(t *testing.T) {
	_, events := runAutopilotHook(t, runPromptSubmit, "2.2.0/user_prompt_submit.json")
	var prompt map[string]interface{}
	for _, event := range events {
		if autopilotAction(event) == "prompt.submitted" {
			prompt = event
		}
	}
	if prompt == nil {
		t.Fatalf("no prompt.submitted event in %#v", events)
	}
	if got := autopilotField(prompt, "session", "id"); got != autopilot220Session {
		t.Fatalf("session.id = %v, want %s", got, autopilot220Session)
	}
	if got := autopilotField(prompt, "harness", "name"); got != "autopilot" {
		t.Fatalf("harness.name = %v, want autopilot", got)
	}
}
