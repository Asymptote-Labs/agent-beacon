package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodexUsageSyncWritesTokenUsageEvent(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "runtime.jsonl")
	statePath := filepath.Join(tmp, "state.json")
	sessionDir := filepath.Join(tmp, "codex", "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-29T10:00:00Z","payload":{"id":"codex-session-1","cwd":"/repo"}}`,
		`{"type":"turn_context","timestamp":"2026-06-29T10:00:01Z","payload":{"model":"gpt-5","turn_id":"turn-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-29T10:00:03Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"output_tokens":50,"cached_input_tokens":600,"reasoning_output_tokens":7}}}}`,
	}
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CODEX_USAGE_STATE", statePath)
	t.Setenv("BEACON_CODEX_SESSIONS_DIR", sessionDir)
	origPlatform := platformFlag
	platformFlag = "codex"
	t.Cleanup(func() { platformFlag = origPlatform })

	runHookWithInput(t, runCodexUsageSync, map[string]interface{}{"hook_event_name": "Stop"})
	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one token event", events)
	}
	event := events[0]
	if event["message"] != "Codex token usage observed" {
		t.Fatalf("message = %#v", event["message"])
	}
	session := event["session"].(map[string]interface{})
	if session["id"] != "codex:codex-session-1" {
		t.Fatalf("session = %#v", session)
	}
	usage := event["gen_ai"].(map[string]interface{})["usage"].(map[string]interface{})
	if usage["input_tokens"].(float64) != 400 || usage["output_tokens"].(float64) != 50 {
		t.Fatalf("usage = %#v", usage)
	}
	cacheRead := usage["cache_read"].(map[string]interface{})
	if cacheRead["input_tokens"].(float64) != 600 {
		t.Fatalf("cache_read = %#v", cacheRead)
	}
	reasoning := usage["reasoning"].(map[string]interface{})
	if reasoning["output_tokens"].(float64) != 7 {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if _, ok := usage["cost_usd"]; ok {
		t.Fatalf("Codex usage event should not include cost_usd: %#v", usage)
	}
	raw := event["raw"].(map[string]interface{})
	if raw["source"] != "codex_session_jsonl" || raw["dedup_key"] == "" {
		t.Fatalf("raw = %#v", raw)
	}

	runHookWithInput(t, runCodexUsageSync, map[string]interface{}{"hook_event_name": "Stop"})
	if got := len(endpointEvents(t, logPath)); got != 1 {
		t.Fatalf("idempotent run wrote %d events, want 1", got)
	}
}
