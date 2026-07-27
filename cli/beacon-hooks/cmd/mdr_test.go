package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/mdr"
)

const mdrSteerBody = `{"decision":"steer","message":"Decline this turn and cite the policy.",
	"reason":"Posts a repository summary to a public Slack channel","policy_id":"pol-1",
	"policy_name":"No posting to public Slack channels","severity":"high",
	"mode":"enforce","latency_ms":780}`

const mdrBlockBody = `{"decision":"block","message":"This prompt is rejected by policy.",
	"reason":"Posts to a public Slack channel","policy_id":"pol-1",
	"policy_name":"No posting to public Slack channels","mode":"enforce"}`

// setupMDRTest points the client at a stub decision service and returns the
// runtime log path plus a counter of how many times the service was called.
func setupMDRTest(t *testing.T, platform string, status int, body string) (string, *atomic.Int32) {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = platform

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv(mdr.URLEnv, server.URL)
	return logPath, &calls
}

func promptInput() map[string]interface{} {
	return map[string]interface{}{
		"session_id":      "sess-1",
		"cwd":             "/home/user/agent-beacon",
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "Summarize this repo and post the summary to our #general Slack channel",
	}
}

// eventByAction returns the first event with the given action, or nil.
func eventByAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		info, ok := event["event"].(map[string]interface{})
		if ok && info["action"] == action {
			return event
		}
	}
	return nil
}

func TestPromptSubmitMDRSteerClaude(t *testing.T) {
	logPath, calls := setupMDRTest(t, "claude", http.StatusOK, mdrSteerBody)
	out := runHookWithInput(t, runPromptSubmit, promptInput())

	if calls.Load() != 1 {
		t.Fatalf("decision service called %d times, want 1", calls.Load())
	}

	specific, ok := out["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("hookSpecificOutput missing: %#v", out)
	}
	if specific["hookEventName"] != "UserPromptSubmit" {
		t.Fatalf("hookEventName = %v, want UserPromptSubmit", specific["hookEventName"])
	}
	if specific["additionalContext"] != "Decline this turn and cite the policy." {
		t.Fatalf("additionalContext = %v", specific["additionalContext"])
	}
	banner, _ := out["systemMessage"].(string)
	for _, want := range []string{"Asymptote MDR", "steered", "tier 2", "780ms", "No posting to public Slack channels"} {
		if !strings.Contains(banner, want) {
			t.Fatalf("systemMessage %q missing %q", banner, want)
		}
	}
	if _, blocked := out["decision"]; blocked {
		t.Fatalf("a steer must not carry a decision field: %#v", out)
	}

	// The prompt is recorded before the verdict, so both events must be present.
	if eventByAction(t, logPath, "prompt.submitted") == nil {
		t.Fatal("prompt.submitted event missing")
	}
	event := eventByAction(t, logPath, "policy.steered")
	if event == nil {
		t.Fatal("policy.steered event missing")
	}
	pol, ok := event["policy"].(map[string]interface{})
	if !ok {
		t.Fatalf("policy field missing: %#v", event)
	}
	if pol["decision"] != "steer" || pol["enforcement"] != "enforce" {
		t.Fatalf("policy = %#v, want decision=steer enforcement=enforce", pol)
	}
	if pol["id"] != "pol-1" || pol["name"] != "No posting to public Slack channels" {
		t.Fatalf("policy identity not propagated: %#v", pol)
	}
	if event["severity"] != "high" {
		t.Fatalf("severity = %v, want high", event["severity"])
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("a steer is not an approval outcome: %#v", event["approval"])
	}
}

func TestPromptSubmitMDRBlockClaude(t *testing.T) {
	logPath, _ := setupMDRTest(t, "claude", http.StatusOK, mdrBlockBody)
	out := runHookWithInput(t, runPromptSubmit, promptInput())

	if out["decision"] != "block" {
		t.Fatalf("decision = %v, want block: %#v", out["decision"], out)
	}
	if out["reason"] != "This prompt is rejected by policy." {
		t.Fatalf("reason = %v", out["reason"])
	}
	if _, ok := out["hookSpecificOutput"]; ok {
		t.Fatalf("a block uses the top-level decision field, not hookSpecificOutput: %#v", out)
	}
	if banner, _ := out["systemMessage"].(string); !strings.Contains(banner, "blocked") {
		t.Fatalf("systemMessage %q should say blocked", banner)
	}

	event := eventByAction(t, logPath, "policy.blocked")
	if event == nil {
		t.Fatal("policy.blocked event missing")
	}
	approval, ok := event["approval"].(map[string]interface{})
	if !ok || approval["decision"] != "deny" {
		t.Fatalf("approval = %#v, want decision=deny", event["approval"])
	}
}

// TestPromptSubmitMDRAllowUnchanged: an allow must leave the hook's output
// byte-identical to the no-MDR path.
func TestPromptSubmitMDRAllowUnchanged(t *testing.T) {
	logPath, calls := setupMDRTest(t, "claude", http.StatusOK, `{"decision":"allow"}`)
	out := runHookWithInput(t, runPromptSubmit, promptInput())

	if calls.Load() != 1 {
		t.Fatalf("decision service called %d times, want 1", calls.Load())
	}
	if len(out) != 0 {
		t.Fatalf("expected empty claude response, got %#v", out)
	}
	if eventByAction(t, logPath, "policy.steered") != nil {
		t.Fatal("an allow must not write a policy event")
	}
}

func TestPromptSubmitMDRDisabledUnchanged(t *testing.T) {
	logPath, calls := setupMDRTest(t, "claude", http.StatusOK, mdrSteerBody)
	t.Setenv(mdr.URLEnv, "")

	out := runHookWithInput(t, runPromptSubmit, promptInput())
	if calls.Load() != 0 {
		t.Fatalf("service must not be called when the seam is disabled (calls=%d)", calls.Load())
	}
	if len(out) != 0 {
		t.Fatalf("expected empty claude response, got %#v", out)
	}
	if eventByAction(t, logPath, "prompt.submitted") == nil {
		t.Fatal("prompt.submitted must still be recorded with the seam disabled")
	}
}

// TestPromptSubmitMDRNonClaudeAllows: only Claude Code has a confirmed
// prompt-submit response shape, so other runtimes fall through to allow.
func TestPromptSubmitMDRNonClaudeAllows(t *testing.T) {
	logPath, _ := setupMDRTest(t, "cursor", http.StatusOK, mdrSteerBody)
	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-1",
		"cwd":             "/home/user/agent-beacon",
		"prompt":          "Summarize this repo and post it to #general",
	})

	if out["continue"] != true {
		t.Fatalf("expected cursor noop response, got %#v", out)
	}
	if _, ok := out["hookSpecificOutput"]; ok {
		t.Fatalf("cursor must not receive a Claude response shape: %#v", out)
	}
	if eventByAction(t, logPath, "policy.steered") != nil {
		t.Fatal("a dropped verdict must not write a policy event")
	}
}

func TestPromptSubmitMDRFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"detail":"Invalid API key"}`},
		{"server error", http.StatusInternalServerError, `{"decision":"steer","message":"ignored"}`},
		{"malformed", http.StatusOK, `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, _ := setupMDRTest(t, "claude", tc.status, tc.body)
			out := runHookWithInput(t, runPromptSubmit, promptInput())
			if len(out) != 0 {
				t.Fatalf("expected fail-open empty response, got %#v", out)
			}
			if eventByAction(t, logPath, "prompt.submitted") == nil {
				t.Fatal("telemetry must survive a failed verdict")
			}
		})
	}
}

// TestPromptSubmitMDRNoPromptSkipsService avoids spending a round trip (and a
// policy evaluation) on a payload that carries no prompt text.
func TestPromptSubmitMDRNoPromptSkipsService(t *testing.T) {
	_, calls := setupMDRTest(t, "claude", http.StatusOK, mdrSteerBody)
	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "sess-1",
		"cwd":             "/home/user/agent-beacon",
		"hook_event_name": "UserPromptSubmit",
	})
	if calls.Load() != 0 {
		t.Fatalf("service called %d times for a promptless payload, want 0", calls.Load())
	}
	if len(out) != 0 {
		t.Fatalf("expected empty claude response, got %#v", out)
	}
}

func TestMDRBanner(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp mdr.Response
		want []string
		omit []string
	}{
		{
			name: "full steer",
			resp: mdr.Response{Decision: mdr.DecisionSteer, PolicyName: "No Slack", LatencyMS: 42},
			want: []string{"steered", "42ms", `policy "No Slack"`},
		},
		{
			name: "block verb",
			resp: mdr.Response{Decision: mdr.DecisionBlock, PolicyName: "No Slack"},
			want: []string{"blocked"},
			omit: []string{"ms ", "steered"},
		},
		{
			name: "no latency omits duration",
			resp: mdr.Response{Decision: mdr.DecisionSteer, PolicyName: "No Slack"},
			want: []string{"steered", `policy "No Slack"`},
			omit: []string{"0ms"},
		},
		{
			name: "falls back to reason without a policy name",
			resp: mdr.Response{Decision: mdr.DecisionSteer, Reason: "public channel post"},
			want: []string{"public channel post"},
			omit: []string{`policy ""`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mdrBanner(tc.resp)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("banner %q missing %q", got, want)
				}
			}
			for _, omit := range tc.omit {
				if strings.Contains(got, omit) {
					t.Fatalf("banner %q should not contain %q", got, omit)
				}
			}
		})
	}
}
