package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/policy"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
)

// The provider script is written once per test binary and reused: on macOS the first exec of a
// freshly written executable costs ~55ms in system policy checks, which a per-test script paid on
// every policy test. It lives in TestMain's temp directory, which TestMain removes.
var policyProviderScript struct {
	once sync.Once
	path string
	err  error
}

// policyTestResponseEnv carries the response the shared provider script prints.
const policyTestResponseEnv = "BEACON_TEST_POLICY_RESPONSE"

// writeProvider points BEACON_POLICY_PROVIDER at a shell script that ignores stdin and prints
// responseJSON. Skips on Windows, which has no /bin/sh.
func writeProvider(t *testing.T, responseJSON string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("provider script requires a POSIX shell")
	}
	policyProviderScript.once.Do(func() {
		path := filepath.Join(filepath.Dir(os.Getenv(activesession.PathEnv)), "policy-provider.sh")
		script := "#!/bin/sh\ncat >/dev/null\nprintf '%s' \"$" + policyTestResponseEnv + "\"\n"
		policyProviderScript.path, policyProviderScript.err = path, os.WriteFile(path, []byte(script), 0o755)
	})
	if policyProviderScript.err != nil {
		t.Fatalf("write provider: %v", policyProviderScript.err)
	}
	t.Setenv(policyTestResponseEnv, responseJSON)
	t.Setenv(policy.ProviderEnv, policyProviderScript.path)
}

func setupPolicyTest(t *testing.T, platform, responseJSON string) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = platform
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	writeProvider(t, responseJSON)
	return logPath
}

const denyResponse = `{"decision":"deny","reason":"blocked by test","rule_id":"agent-permission-bypass-spawn","severity":"high","mode":"enforce"}`

func assertDenialEvent(t *testing.T, logPath string) {
	t.Helper()
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.denied" {
		t.Fatalf("event.action = %q, want approval.denied", action)
	}
	pol, ok := event["policy"].(map[string]interface{})
	if !ok {
		t.Fatalf("policy field missing: %#v", event)
	}
	if pol["decision"] != "deny" || pol["enforcement"] != "enforce" {
		t.Fatalf("policy = %#v, want decision=deny enforcement=enforce", pol)
	}
	if pol["id"] != "agent-permission-bypass-spawn" {
		t.Fatalf("policy.id = %v, want rule id propagated", pol["id"])
	}
	approval, ok := event["approval"].(map[string]interface{})
	if !ok || approval["decision"] != "deny" {
		t.Fatalf("approval = %#v, want decision=deny", event["approval"])
	}
}

func TestPreToolPolicyDenyCursor(t *testing.T) {
	logPath := setupPolicyTest(t, "cursor", denyResponse)
	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"conversation_id": "conv-1",
		"command":         "claude --dangerously-skip-permissions -p go",
	})
	if out["permission"] != "deny" {
		t.Fatalf("cursor deny response = %#v, want permission=deny", out)
	}
	assertDenialEvent(t, logPath)
}

// Claude Code's PreToolUse reads permissionDecision and ignores a decision object, so the
// permission-phase shape must not leak into pre-tool.
func TestPreToolPolicyDenyClaude(t *testing.T) {
	logPath := setupPolicyTest(t, "claude", denyResponse)
	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"session_id": "s-claude",
		"tool_name":  "Bash",
		"tool_input": map[string]interface{}{"command": "codex --full-auto"},
	})
	hso, ok := out["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("claude deny response = %#v, want hookSpecificOutput", out)
	}
	if hso["permissionDecision"] != "deny" || hso["permissionDecisionReason"] != "blocked by test" {
		t.Fatalf("hookSpecificOutput = %#v, want deny + reason", hso)
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Fatalf("hookEventName = %v, want PreToolUse", hso["hookEventName"])
	}
	if _, ok := hso["decision"]; ok {
		t.Fatalf("hookSpecificOutput = %#v carries the PermissionRequest decision object", hso)
	}
	assertDenialEvent(t, logPath)
}

func TestPreToolPolicyDenyDevin(t *testing.T) {
	logPath := setupPolicyTest(t, "devin", denyResponse)
	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"session_id": "s-devin",
		"tool_name":  "exec",
		"tool_input": map[string]interface{}{"command": "goose --yolo"},
	})
	if out["decision"] != "reject" {
		t.Fatalf("devin deny response = %#v, want decision=reject", out)
	}
	assertDenialEvent(t, logPath)
}

func TestPreToolPolicyAllowProceedsNormally(t *testing.T) {
	logPath := setupPolicyTest(t, "cursor", `{"decision":"allow"}`)
	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"conversation_id": "conv-allow",
		"command":         "ls -la",
	})
	if out["permission"] != "allow" {
		t.Fatalf("allow response = %#v, want permission=allow", out)
	}
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action == "approval.denied" {
		t.Fatalf("provider allowed but a denial was emitted: %#v", event)
	}
}

func TestPermissionRequestPolicyDenyClaude(t *testing.T) {
	// Regression: Claude is a confirmed-deny platform, so a provider deny on the
	// permission-request hook must be honored (not silently allowed) just like on
	// pre-tool. Claude Code's PermissionRequest reads only
	// hookSpecificOutput.decision.behavior, so the PreToolUse permissionDecision
	// object would be ignored there while the event still claimed enforcement.
	logPath := setupPolicyTest(t, "claude", denyResponse)
	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"session_id": "s-claude-perm",
		"tool_name":  "NotebookEdit",
		"tool_input": map[string]interface{}{"notebook_path": "/tmp/n.ipynb"},
	})
	hso, ok := out["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("claude permission-request deny = %#v, want hookSpecificOutput", out)
	}
	if hso["hookEventName"] != "PermissionRequest" {
		t.Fatalf("hookEventName = %v, want PermissionRequest", hso["hookEventName"])
	}
	if _, ok := hso["permissionDecision"]; ok {
		t.Fatalf("hookSpecificOutput = %#v carries the PreToolUse permissionDecision", hso)
	}
	decision, ok := hso["decision"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput = %#v, want a decision object", hso)
	}
	if decision["behavior"] != "deny" || decision["message"] != "blocked by test" {
		t.Fatalf("decision = %#v, want behavior=deny message=blocked by test", decision)
	}
	assertDenialEvent(t, logPath)
}

func TestPermissionRequestPolicyDenyDevin(t *testing.T) {
	logPath := setupPolicyTest(t, "devin", denyResponse)
	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"session_id": "s-devin-perm",
		"tool_name":  "exec",
		"tool_input": map[string]interface{}{"command": "claude --dangerously-skip-permissions"},
	})
	if out["decision"] != "reject" {
		t.Fatalf("devin permission-request deny = %#v, want decision=reject", out)
	}
	assertDenialEvent(t, logPath)
}
