package cmd

import (
	"strings"
	"testing"
)

// Codex hooks installed before inventory became a scheduled job still run
// `beacon-hooks --platform codex inventory-heartbeat` on every session start and prompt, and only a
// hooks reinstall rewrites them. The command must keep parsing and exit 0 with the empty response,
// writing nothing: an unknown subcommand would fail every one of those hooks. Regression for
// 5e6bbec9 ("keep the old hook commands parsing").
func TestRetiredInventoryHeartbeatStillAnswersOldCodexHooks(t *testing.T) {
	t.Setenv("BEACON_ENDPOINT_CLI", "/definitely/not/a/binary")
	logPath, stdout := runHookProcess(t, map[string]interface{}{"session_id": "s1", "cwd": "/repo"},
		"--platform", "codex", "inventory-heartbeat")
	if got := strings.TrimSpace(string(stdout)); got != "{}" {
		t.Fatalf("stdout = %q, want the empty response", got)
	}
	if events := endpointEventsIfAny(t, logPath); len(events) != 0 {
		t.Fatalf("the retired command wrote %d events: %#v", len(events), events)
	}
}
