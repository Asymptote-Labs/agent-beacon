package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	hookconfig "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/state"
)

func TestRunPreToolAllowsAndEmitsTelemetry(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
	}{
		{
			name: "missing session allows",
		},
		{
			name:      "session allows",
			sessionID: "conv-observe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupHookConfigDirs(t)
			platformFlag = "cursor"

			input := map[string]interface{}{}
			if tt.sessionID != "" {
				input["conversation_id"] = tt.sessionID
			}
			out := runHookWithInput(t, runPreTool, input)

			if out["permission"] != "allow" {
				t.Fatalf("permission = %v, want allow; output=%#v", out["permission"], out)
			}
		})
	}
}

func TestRunPromptSubmitUsesCursorResponse(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{"conversation_id": "conv-submit"})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}
}

func TestRunPromptSubmitEmitsFactoryPromptEvent(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "factory"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"session_id":      "factory-session",
		"transcript_path": "/tmp/factory-session.jsonl",
		"cwd":             "/repo",
		"hook_event_name": "UserPromptSubmit",
		"permission_mode": "off",
		"prompt":          "summarize token=prompt-secret",
	})
	if len(out) != 0 {
		t.Fatalf("factory prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if got := event["message"]; got != "Prompt submitted to agent" {
		t.Fatalf("message = %q, want prompt submitted", got)
	}
	harness := event["harness"].(map[string]interface{})
	if harness["name"] != "factory" {
		t.Fatalf("harness = %#v, want factory", harness)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	prompt := event["prompt"].(map[string]interface{})
	if got := prompt["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
}

func TestRunPromptSubmitEmitsDevinPromptWithoutSession(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	t.Setenv("DEVIN_PROJECT_DIR", "/repo")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"hook_event_name": "UserPromptSubmit",
		"prompt":          "summarize token=devin-secret",
	})
	if len(out) != 0 {
		t.Fatalf("devin prompt response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if _, ok := event["session"].(map[string]interface{})["id"]; ok {
		t.Fatalf("devin event should not invent session id: %#v", event["session"])
	}
	if got := event["repository"]; got != "/repo" {
		t.Fatalf("repository = %q, want DEVIN_PROJECT_DIR", got)
	}
}

func TestRunPreToolEmitsDevinToolTelemetryWithoutApproval(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPreTool, map[string]interface{}{
		"hook_event_name": "PreToolUse",
		"tool_name":       "exec",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if len(out) != 0 {
		t.Fatalf("devin pre-tool response = %#v, want empty response", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "tool.invoked" {
		t.Fatalf("event.action = %q, want tool.invoked", action)
	}
	if _, ok := event["approval"]; ok {
		t.Fatalf("PreToolUse should not emit approval telemetry: %#v", event["approval"])
	}
	if command := event["command"].(map[string]interface{})["command"]; command != "git status" {
		t.Fatalf("command = %q, want git status", command)
	}
}

func TestRunPermissionRequestEmitsDevinApproval(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "PermissionRequest",
		"tool_name":       "exec",
		"tool_input": map[string]interface{}{
			"command": "git status",
		},
	})
	if out["decision"] != "approve" {
		t.Fatalf("devin permission response = %#v, want decision=approve", out)
	}

	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "approval.allowed" {
		t.Fatalf("event.action = %q, want approval.allowed", action)
	}
	approval := event["approval"].(map[string]interface{})
	if approval["decision"] != "approve" {
		t.Fatalf("approval = %#v, want approve", approval)
	}
}

func TestRunPermissionRequestNoopsForNonDevinPlatform(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runPermissionRequest, map[string]interface{}{
		"hook_event_name": "PermissionRequest",
		"tool_name":       "Shell",
	})
	if len(out) != 0 {
		t.Fatalf("non-Devin permission response = %#v, want empty response", out)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("non-Devin permission request should not write endpoint log")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected endpoint log stat error: %v", err)
	}
}

func TestRunSessionLifecycleEmitsDevinEventsWithoutSession(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "devin"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("DEVIN_PROJECT_DIR", "/repo")

	runHookWithInput(t, runSessionStart, map[string]interface{}{"source": "startup"})
	runHookWithInput(t, runStop, map[string]interface{}{"stop_hook_active": false})
	runHookWithInput(t, runSessionEnd, map[string]interface{}{"reason": "exit"})

	events := endpointEvents(t, logPath)
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3: %#v", len(events), events)
	}
	want := []string{"session.started", "tool.completed", "session.ended"}
	for i, action := range want {
		if got := events[i]["event"].(map[string]interface{})["action"]; got != action {
			t.Fatalf("event[%d].action = %q, want %q", i, got, action)
		}
		if session, ok := events[i]["session"].(map[string]interface{}); ok {
			if _, hasID := session["id"]; hasID {
				t.Fatalf("event[%d] should not invent session id: %#v", i, session)
			}
		}
	}
}

func TestRunPromptSubmitEmitsTypedPromptForFullRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ENDPOINT_CONFIG", filepath.Join(t.TempDir(), "missing-config.json"))

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-submit",
		"prompt":          "summarize token=prompt-secret",
	})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}

	event := lastEndpointEvent(t, logPath)
	prompt, ok := event["prompt"].(map[string]interface{})
	if !ok {
		t.Fatalf("prompt field missing: %#v", event)
	}
	if got := prompt["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if _, ok := event["raw"]; ok {
		t.Fatalf("raw should not carry prompt payload: %#v", event["raw"])
	}
	if events := endpointEvents(t, logPath); len(events) != 1 {
		t.Fatalf("endpoint event count = %d, want 1; events=%#v", len(events), events)
	}
}

func TestRunPromptSubmitOmitsPromptForMetadataRetention(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-submit",
		"prompt":          "summarize this file",
	})
	if out["continue"] != true {
		t.Fatalf("cursor prompt response = %#v, want continue=true", out)
	}

	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt field: %#v", event["prompt"])
	}
}

func TestRunSessionStartStoresModel(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	out := runHookWithInput(t, runSessionStart, map[string]interface{}{
		"conversation_id": "conv-model",
		"model":           "gpt-5.5",
	})
	if len(out) != 0 {
		t.Fatalf("session-start response = %#v, want empty response", out)
	}
	if got := state.NewSessionState("conv-model", "cursor").GetModel(); got != "gpt-5.5" {
		t.Fatalf("stored model = %q, want gpt-5.5", got)
	}
}

func TestRunSessionEndRemovesSessionLog(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"
	logFile := hookconfig.GetSessionLogFile("cursor", "conv-end")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		t.Fatalf("mkdir session log dir: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("session log"), 0644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	out := runHookWithInput(t, runSessionEnd, map[string]interface{}{"conversation_id": "conv-end"})
	if len(out) != 0 {
		t.Fatalf("session-end response = %#v, want empty response", out)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("session log still exists or unexpected error: %v", err)
	}
}

func setupHookConfigDirs(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	origBeaconDir := hookconfig.BeaconDir
	origClaudeDir := hookconfig.ClaudeDir
	origCopilotDir := hookconfig.CopilotDir
	origCursorDir := hookconfig.CursorDir
	origDevinDir := hookconfig.DevinDir
	origFactoryDir := hookconfig.FactoryDir
	origGrokDir := hookconfig.GrokDir
	origOpenCodeDir := hookconfig.OpenCodeDir
	origPlatform := platformFlag
	hookconfig.BeaconDir = tmp
	hookconfig.ClaudeDir = filepath.Join(tmp, "claude")
	hookconfig.CopilotDir = filepath.Join(tmp, "copilot")
	hookconfig.CursorDir = filepath.Join(tmp, "cursor")
	hookconfig.DevinDir = filepath.Join(tmp, "devin")
	hookconfig.FactoryDir = filepath.Join(tmp, "factory")
	hookconfig.GrokDir = filepath.Join(tmp, "grok")
	hookconfig.OpenCodeDir = filepath.Join(tmp, "opencode")
	t.Cleanup(func() {
		hookconfig.BeaconDir = origBeaconDir
		hookconfig.ClaudeDir = origClaudeDir
		hookconfig.CopilotDir = origCopilotDir
		hookconfig.CursorDir = origCursorDir
		hookconfig.DevinDir = origDevinDir
		hookconfig.FactoryDir = origFactoryDir
		hookconfig.GrokDir = origGrokDir
		hookconfig.OpenCodeDir = origOpenCodeDir
		platformFlag = origPlatform
	})
}

func runHookWithInput(t *testing.T, run func(cmd *cobra.Command, args []string), input map[string]interface{}) map[string]interface{} {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = stdinR
	os.Stdout = stdoutW
	defer func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = stdinR.Close()
		_ = stdoutR.Close()
	}()

	if err := json.NewEncoder(stdinW).Encode(input); err != nil {
		t.Fatalf("encode input: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	run(nil, nil)

	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdin = origStdin
	os.Stdout = origStdout
	var out map[string]interface{}
	if err := json.NewDecoder(stdoutR).Decode(&out); err != nil {
		t.Fatalf("decode hook output: %v", err)
	}
	return out
}

func lastEndpointEvent(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	events := endpointEvents(t, path)
	if len(events) == 0 {
		t.Fatal("endpoint log was empty")
	}
	return events[len(events)-1]
}

func endpointEvents(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read endpoint log: %v", err)
	}
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatal("endpoint log was empty")
	}
	events := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode endpoint event: %v", err)
		}
		events = append(events, event)
	}
	return events
}
