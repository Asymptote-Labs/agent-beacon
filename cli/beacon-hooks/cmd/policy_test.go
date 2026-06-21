package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/policy"
)

// writeProvider writes an executable shell script that ignores stdin and prints a
// fixed JSON response, then points BEACON_POLICY_PROVIDER at it. Skips on Windows,
// which has no /bin/sh.
func writeProvider(t *testing.T, responseJSON string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("provider script requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "provider.sh")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '" + responseJSON + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	t.Setenv(policy.ProviderEnv, path)
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
	// pre-tool.
	logPath := setupPolicyTest(t, "claude", denyResponse)
	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"session_id": "s-claude-perm",
		"tool_name":  "Bash",
		"tool_input": map[string]interface{}{"command": "claude --dangerously-skip-permissions"},
	})
	hso, ok := out["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("claude permission-request deny = %#v, want hookSpecificOutput", out)
	}
	if hso["permissionDecision"] != "deny" {
		t.Fatalf("hookSpecificOutput = %#v, want permissionDecision=deny", hso)
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
