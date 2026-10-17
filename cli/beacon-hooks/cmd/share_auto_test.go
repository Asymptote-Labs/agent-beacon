package cmd

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/activesession"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/shareauto"
)

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("hook made a network request: %s %s", r.Method, r.URL)
	return nil, errors.New("no network from hooks")
}

// shareAutoHookEnv points the runtime log, the active-session record and the share settings at a
// temp directory, and fails the test on any HTTP request made through the default transport.
func shareAutoHookEnv(t *testing.T, settings *shareauto.Settings) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "claude"
	t.Cleanup(func() { platformFlag = "claude" })
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv(activesession.PathEnv, filepath.Join(dir, activesession.FileName))
	settingsPath := filepath.Join(dir, "share.json")
	t.Setenv(shareauto.PathEnv, settingsPath)
	if settings != nil {
		if err := shareauto.Save(settingsPath, *settings); err != nil {
			t.Fatal(err)
		}
	}
	prev := http.DefaultTransport
	http.DefaultTransport = failingTransport{t}
	t.Cleanup(func() { http.DefaultTransport = prev })
	return logPath
}

func runClaudeSessionHooks(t *testing.T, sessionID string) {
	t.Helper()
	runHookWithInput(t, runSessionStart, map[string]interface{}{
		"hook_event_name": "SessionStart", "session_id": sessionID, "cwd": "/work/project",
	})
	runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hook_event_name": "UserPromptSubmit", "session_id": sessionID, "cwd": "/work/project", "prompt": "hello",
	})
	runHookWithInput(t, runPostTool, map[string]interface{}{
		"hook_event_name": "PostToolUse", "session_id": sessionID, "cwd": "/work/project",
		"tool_name": "Bash", "tool_input": map[string]interface{}{"command": "ls"}, "tool_response": map[string]interface{}{"stdout": "a"},
	})
}

func shareRequests(t *testing.T, logPath string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, event := range endpointEvents(t, logPath) {
		if info, _ := event["event"].(map[string]interface{}); info["action"] == asymptoteobserve.ActionShareRequested {
			out = append(out, event)
		}
	}
	return out
}

func TestAutoShareWritesOneRequestPerSession(t *testing.T) {
	logPath := shareAutoHookEnv(t, &shareauto.Settings{Auto: shareauto.Auto{Enabled: true, Visibility: "private"}})
	runClaudeSessionHooks(t, "sess-a")
	runClaudeSessionHooks(t, "sess-a")
	runClaudeSessionHooks(t, "sess-b")

	requests := shareRequests(t, logPath)
	if len(requests) != 2 {
		t.Fatalf("share requests = %d, want one per session", len(requests))
	}
	first := requests[0]
	if got := first["session"].(map[string]interface{})["id"]; got != "sess-a" {
		t.Fatalf("session = %v", got)
	}
	if got := first["share"].(map[string]interface{})["visibility"]; got != "private" {
		t.Fatalf("share.visibility = %v", got)
	}
	info := first["event"].(map[string]interface{})
	if info["fidelity"] != "inferred" || info["category"] != "session" {
		t.Fatalf("event block = %v", info)
	}
	if got := first["harness"].(map[string]interface{})["name"]; got != "claude_code" {
		t.Fatalf("harness = %v", got)
	}
	// It follows the session's first event, which is SessionStart here.
	events := endpointEvents(t, logPath)
	if events[0]["event"].(map[string]interface{})["action"] != "session.started" || events[1]["event"].(map[string]interface{})["action"] != asymptoteobserve.ActionShareRequested {
		t.Fatalf("order: %v then %v", events[0]["event"], events[1]["event"])
	}
}

func TestAutoShareOffWritesNothing(t *testing.T) {
	logPath := shareAutoHookEnv(t, nil)
	runClaudeSessionHooks(t, "sess-a")
	if n := len(shareRequests(t, logPath)); n != 0 {
		t.Fatalf("share requests with auto-share off = %d", n)
	}
	logPath = shareAutoHookEnv(t, &shareauto.Settings{Auto: shareauto.Auto{Enabled: false}})
	runClaudeSessionHooks(t, "sess-c")
	if n := len(shareRequests(t, logPath)); n != 0 {
		t.Fatalf("share requests with auto-share disabled = %d", n)
	}
}

func TestAutoShareHonorsProjectOptOut(t *testing.T) {
	logPath := shareAutoHookEnv(t, &shareauto.Settings{
		Auto:     shareauto.Auto{Enabled: true},
		Projects: []shareauto.Project{{Path: "/work/project", Enabled: false}},
	})
	runClaudeSessionHooks(t, "sess-a")
	if n := len(shareRequests(t, logPath)); n != 0 {
		t.Fatalf("share requests in an opted-out project = %d", n)
	}
}
