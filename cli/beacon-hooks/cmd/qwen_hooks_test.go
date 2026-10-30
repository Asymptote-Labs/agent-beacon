package cmd

import (
	"path/filepath"
	"testing"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// Qwen Code's hook payloads are the Claude Code shape: snake_case base fields, `tool_name` /
// `tool_input`, `cwd`. Most of the adapter therefore needs no Qwen-specific reader -- the default
// branches already do the right thing. These tests exist because that is a fact about Qwen's
// contract rather than an accident of the code: they pin the default readers to Qwen payloads so a
// future refactor of the claude branches cannot silently take Qwen's session id, working directory
// or tool fields with it.

// Qwen gets its own state directory. Sharing Claude's would mix two runtimes' session state and
// hook logs in one place: a stale-evaluation cleanup for a Qwen session would walk Claude's
// sessions, and `~/.beacon/claude/hooks.log` would interleave two runtimes.
func TestQwenHasItsOwnStateDirectory(t *testing.T) {
	setupHookConfigDirs(t)

	stateDir := hookconfig.GetStateDir("qwen")
	if stateDir != hookconfig.QwenDir {
		t.Fatalf("GetStateDir(qwen) = %q, want QwenDir %q", stateDir, hookconfig.QwenDir)
	}
	if stateDir == hookconfig.ClaudeDir {
		t.Fatalf("GetStateDir(qwen) = %q, want a directory distinct from Claude's", stateDir)
	}
	if got, want := hookconfig.GetLogFile("qwen"), filepath.Join(hookconfig.QwenDir, "hooks.log"); got != want {
		t.Fatalf("GetLogFile(qwen) = %q, want %q", got, want)
	}
	if got, want := hookconfig.GetSessionLogFile("qwen", "s1"), filepath.Join(hookconfig.QwenDir, "logs", "s1.log"); got != want {
		t.Fatalf("GetSessionLogFile(qwen) = %q, want %q", got, want)
	}
}

// The security property behind routing Qwen through the claude branch of preToolResponse.
//
// Qwen reads a PreToolUse decision from `hookSpecificOutput.permissionDecision`, and "allow" there
// means run the tool *without the usual approval prompt*. A telemetry hook that answered "allow"
// would silently disarm the user's own permission prompts for every tool call on a runtime where
// the hook was installed only to watch. The empty object carries no decision, so Qwen's normal
// permission flow runs untouched.
func TestQwenPreToolDoesNotApproveOnBehalfOfTheUser(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "qwen"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"session_id":      "qwen-session-1",
		"cwd":             "/tmp/qwen-project",
		"hook_event_name": "PreToolUse",
		"tool_name":       "run_shell_command",
		"tool_input":      map[string]interface{}{"command": "rm -rf build"},
	})
	if len(out) != 0 {
		t.Fatalf("pre-tool response = %#v, want an empty object carrying no permission decision", out)
	}
	if _, ok := out["hookSpecificOutput"]; ok {
		t.Fatalf("pre-tool response carried hookSpecificOutput: %#v", out)
	}
	if _, ok := out["permission"]; ok {
		t.Fatalf("pre-tool response carried a Cursor-shaped permission field: %#v", out)
	}

	// Observed, not approved: the event records the invocation without claiming an approval
	// decision Beacon did not make.
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "command.executed" && action != "tool.invoked" {
		t.Fatalf("event.action = %q, want the tool invocation recorded", action)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("pre-tool event claimed an approval decision: %#v", event["approval"])
	}
	if got := event["session"].(map[string]interface{})["id"]; got != "qwen-session-1" {
		t.Fatalf("session.id = %q, want qwen-session-1", got)
	}
	if got := event["session"].(map[string]interface{})["working_directory"]; got != "/tmp/qwen-project" {
		t.Fatalf("working_directory = %q, want /tmp/qwen-project", got)
	}
}

// The policy seam is off by default and fails open, but when a provider does deny, the response has
// to be the shape Qwen actually reads or the deny is silently dropped and the tool runs anyway.
func TestQwenPolicyDenyUsesQwensPermissionDecisionShape(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "qwen"

	deny := policyDenyResponse("Security policy blocks database writes", policycontract.PhasePreTool)
	if deny == nil {
		t.Fatal("policyDenyResponse(qwen) = nil; a provider deny would be silently dropped and the tool would run")
	}
	specific, ok := deny["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("deny response = %#v, want hookSpecificOutput", deny)
	}
	if got := specific["hookEventName"]; got != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", got)
	}
	if got := specific["permissionDecision"]; got != "deny" {
		t.Errorf("permissionDecision = %q, want deny", got)
	}
	// Qwen's contract marks permissionDecisionReason REQUIRED alongside the decision.
	if got := specific["permissionDecisionReason"]; got != "Security policy blocks database writes" {
		t.Errorf("permissionDecisionReason = %q, want the provider's reason", got)
	}
}

// Qwen's PermissionRequest event reads `hookSpecificOutput.decision.behavior`, not
// permissionDecision, so the PreToolUse object sent from the permission phase would be ignored and
// the deny recorded as enforced would never take effect.
func TestQwenPermissionRequestPolicyDenyUsesDecisionObject(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "qwen"

	deny := policyDenyResponse("Security policy blocks database writes", policycontract.PhasePermissionRequest)
	specific, ok := deny["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("deny response = %#v, want hookSpecificOutput", deny)
	}
	if _, ok := specific["permissionDecision"]; ok {
		t.Errorf("hookSpecificOutput = %#v carries the PreToolUse permissionDecision on PermissionRequest", specific)
	}
	decision, ok := specific["decision"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput = %#v, want a decision object", specific)
	}
	if decision["behavior"] != "deny" || decision["message"] != "Security policy blocks database writes" {
		t.Errorf("decision = %#v, want behavior=deny with the provider's reason as message", decision)
	}
}
