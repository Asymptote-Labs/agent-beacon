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

// Pi's tool_call is its only blockable pre-execution event, so the seam can be honored there with
// full fidelity: the response is Pi's native {block, reason} shape, which the extension returns to
// Pi unchanged rather than translating.
func TestPiEventPolicyDeny(t *testing.T) {
	logPath := setupPolicyTest(t, "pi", denyResponse)
	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_call",
		"session_id": "pi-deny-1",
		"cwd":        "/repo",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "claude --dangerously-skip-permissions -p go"},
	})
	if out["block"] != true {
		t.Fatalf("pi deny response = %#v, want block=true", out)
	}
	if out["reason"] != "blocked by test" {
		t.Fatalf("pi deny reason = %#v, want the provider's reason", out["reason"])
	}
	assertDenialEvent(t, logPath)
}

// A denied call never ran, so tool.invoked must not be written for it. The log would otherwise carry
// an event for something that did not happen, next to the denial that says it did not.
func TestPiEventPolicyDenyDoesNotRecordToolInvoked(t *testing.T) {
	logPath := setupPolicyTest(t, "pi", denyResponse)
	runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_call",
		"session_id": "pi-deny-2",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "codex --full-auto"},
	})

	for _, event := range endpointEvents(t, logPath) {
		if action := eventAction(t, event); action == "tool.invoked" {
			t.Fatal("a denied tool call was also recorded as invoked")
		}
	}
}

func TestPiEventPolicyAllowProceedsNormally(t *testing.T) {
	logPath := setupPolicyTest(t, "pi", `{"decision":"allow"}`)
	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_call",
		"session_id": "pi-allow-1",
		"cwd":        "/repo",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "ls -la"},
	})
	if len(out) != 0 {
		t.Fatalf("pi allow response = %#v, want an empty object", out)
	}
	if got := eventAction(t, lastEndpointEvent(t, logPath)); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked on an allow", got)
	}
}

// The seam runs on the pre-execution event only. Asking about a tool_result would be asking about
// work already done, and a deny there could not stop anything while still blocking the telemetry
// for it.
func TestPiEventPolicyIgnoresToolResult(t *testing.T) {
	logPath := setupPolicyTest(t, "pi", denyResponse)
	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_result",
		"session_id": "pi-result-1",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "ls -la"},
	})
	if len(out) != 0 {
		t.Fatalf("tool_result response = %#v, want an empty object even with a denying provider", out)
	}
	if got := eventAction(t, lastEndpointEvent(t, logPath)); got != "command.executed" {
		t.Fatalf("event.action = %q, want the result recorded normally", got)
	}
}

// With no provider configured -- the open build's default -- the seam is inert and a tool_call is
// pure observation. This is the property that keeps enforcement out of the shipped build.
func TestPiEventWithoutProviderNeverBlocks(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "pi"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv(policy.ProviderEnv, "")

	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_call",
		"session_id": "pi-noprovider",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "rm -rf /"},
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want an empty object with no provider configured", out)
	}
	if got := eventAction(t, lastEndpointEvent(t, logPath)); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", got)
	}
}

// Fail-open on a broken provider. A provider that cannot run must not stop the agent, so a
// non-executable path is an allow -- the same direction every other error path in the seam takes.
func TestPiEventPolicyFailsOpenOnBrokenProvider(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "pi"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv(policy.ProviderEnv, filepath.Join(t.TempDir(), "does-not-exist"))

	out := runHookWithInput(t, runPiEvent, map[string]interface{}{
		"type":       "tool_call",
		"session_id": "pi-broken",
		"tool_name":  "bash",
		"tool_input": map[string]interface{}{"command": "ls"},
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want an empty object when the provider cannot run", out)
	}
	if got := eventAction(t, lastEndpointEvent(t, logPath)); got != "tool.invoked" {
		t.Fatalf("event.action = %q, want the call allowed and recorded", got)
	}
}
