package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// claudeBashFailurePayload is the PostToolUseFailure payload Claude Code's hook documentation
// gives for a failed Bash command: no tool_response, and the exit status and output in `error`.
func claudeBashFailurePayload(errText string) map[string]interface{} {
	return map[string]interface{}{
		"session_id":      "s-743",
		"cwd":             "/repo",
		"permission_mode": "default",
		"hook_event_name": "PostToolUseFailure",
		"tool_name":       "Bash",
		"tool_input":      map[string]interface{}{"command": "npm test", "description": "Run test suite"},
		"tool_use_id":     "toolu_743",
		"error":           errText,
		"is_interrupt":    false,
		"duration_ms":     float64(4187),
	}
}

// runClaudePostTool runs one Claude payload through the post-tool command against a log that
// already holds the given lines, and returns the log's path and every event in it.
func runClaudePostTool(t *testing.T, payload map[string]interface{}, existing ...string) (string, []map[string]interface{}) {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	if len(existing) > 0 {
		var data []byte
		for _, line := range existing {
			data = append(data, line+"\n"...)
		}
		if err := os.WriteFile(logPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runHookWithInput(t, runPostTool, payload)
	return logPath, endpointEvents(t, logPath)
}

// claudeCommandLine is a command.executed line for the call in claudeBashFailurePayload, in the
// shape another capture path writes it.
func claudeCommandLine(t *testing.T, fields map[string]interface{}) string {
	t.Helper()
	event := map[string]interface{}{
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event":          map[string]interface{}{"action": "command.executed", "category": "command"},
		"tool":           map[string]interface{}{"name": "Bash", "command": "npm test"},
		"gen_ai":         map[string]interface{}{"tool": map[string]interface{}{"call": map[string]interface{}{"id": "toolu_743"}, "name": "Bash"}},
	}
	for key, value := range fields {
		event[key] = value
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestClaudeBashPostToolCapturesTypedResult(t *testing.T) {
	payload := postToolPayload("Bash", map[string]interface{}{"command": "sh scripts/probe.sh"},
		map[string]interface{}{"stdout": "Shell output marker.\n", "stderr": "warning\n", "interrupted": false, "isImage": false})
	payload["tool_use_id"] = "toolu_live_bash_1"
	raw, event := runPostToolOnce(t, "claude", payload)

	if event.Event.Action != "command.executed" || event.Severity != "info" {
		t.Fatalf("event = %s/%s, want info command.executed", event.Event.Action, event.Severity)
	}
	if event.Command == nil || event.Command.Command != "sh scripts/probe.sh" ||
		event.Command.Output != "Shell output marker.\nwarning\n" {
		t.Fatalf("command = %+v, want command and captured stdout/stderr", event.Command)
	}
	if event.Command.ExitCode != nil {
		t.Fatalf("command.exit_code = %v, want absent because a success reports no exit code", *event.Command.ExitCode)
	}
	if toolCallResult(event) != nil {
		t.Fatalf("gen_ai.tool.call.result = %#v, want no duplicate of command.output", toolCallResult(event))
	}
	if event.GenAI == nil || event.GenAI.Tool == nil || event.GenAI.Tool.Call == nil || event.GenAI.Tool.Call.ID != "toolu_live_bash_1" {
		t.Fatalf("gen_ai = %+v, want Claude's tool_use_id as the call id", event.GenAI)
	}
	if _, ok := raw["raw"]; ok {
		t.Fatalf("raw = %v, want none for a command that was not interrupted", raw["raw"])
	}
}

// stdout and stderr arrive as separate strings; they are stored stdout first, joined by one
// newline when stdout does not already end in one.
func TestClaudeBashPostToolJoinsStreams(t *testing.T) {
	cases := []struct {
		name, stdout, stderr, want string
	}{
		{"stdout without a trailing newline", "out", "err", "out\nerr"},
		{"stdout with a trailing newline", "out\n", "err\n", "out\nerr\n"},
		{"stderr only", "", "err\n", "err\n"},
		{"stdout only", "out", "", "out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, event := runPostToolOnce(t, "claude", postToolPayload("Bash",
				map[string]interface{}{"command": "sh scripts/probe.sh"},
				map[string]interface{}{"stdout": tc.stdout, "stderr": tc.stderr, "interrupted": false}))
			if event.Command == nil || event.Command.Output != tc.want {
				t.Fatalf("command = %+v, want output %q", event.Command, tc.want)
			}
		})
	}
}

// A failed command reaches the hook with its exit status and interleaved output in `error`. It is
// recorded as the command execution it was, with the exit code Claude reported, rather than as a
// tool failure carrying nothing.
func TestClaudeBashFailureRecordsExitCodeAndOutput(t *testing.T) {
	_, event := runPostToolOnce(t, "claude", claudeBashFailurePayload("Exit code 1\nError: Cannot find module 'express'"))

	if event.Event.Action != "command.executed" || event.Severity != "high" {
		t.Fatalf("event = %s/%s, want high-severity command.executed", event.Event.Action, event.Severity)
	}
	if event.Command == nil || event.Command.Command != "npm test" || event.Command.Output != "Error: Cannot find module 'express'" {
		t.Fatalf("command = %+v, want the command and its output without the exit line", event.Command)
	}
	if event.Command.ExitCode == nil || *event.Command.ExitCode != 1 {
		t.Fatalf("command.exit_code = %v, want 1 from Claude's exit line", event.Command.ExitCode)
	}
	if event.Content == nil || !event.Content.Included || event.Content.Bytes != len("Error: Cannot find module 'express'") {
		t.Fatalf("content = %+v, want a marker describing the retained output", event.Content)
	}
}

// Without an `Exit code N` line the shell never started, so there is no command output to keep.
func TestClaudeBashFailureWithoutExitLineStaysAToolFailure(t *testing.T) {
	_, event := runPostToolOnce(t, "claude", claudeBashFailurePayload("spawn /bin/zsh ENOENT"))

	if event.Event.Action != "tool.failed" || event.Severity != "high" {
		t.Fatalf("event = %s/%s, want high-severity tool.failed", event.Event.Action, event.Severity)
	}
	if event.Command != nil && (event.Command.Output != "" || event.Command.ExitCode != nil) {
		t.Fatalf("command = %+v, want no output or exit code for a shell that never started", event.Command)
	}
}

// An interrupted command's output is partial, which a reader has to be told.
func TestClaudeBashInterruptedOutputIsMarked(t *testing.T) {
	t.Run("success with interrupted", func(t *testing.T) {
		raw, event := runPostToolOnce(t, "claude", postToolPayload("Bash",
			map[string]interface{}{"command": "sleep 60"},
			map[string]interface{}{"stdout": "partial", "stderr": "", "interrupted": true}))
		if event.Event.Action != "command.executed" || event.Command == nil || event.Command.Output != "partial" {
			t.Fatalf("event = %s command = %+v, want command.executed with the partial output", event.Event.Action, event.Command)
		}
		if got := nested(t, raw, "raw")["claude_interrupted"]; got != true {
			t.Fatalf("raw.claude_interrupted = %v, want true", got)
		}
	})
	t.Run("failure with is_interrupt", func(t *testing.T) {
		payload := claudeBashFailurePayload("Exit code 130\npartial")
		payload["is_interrupt"] = true
		raw, _ := runPostToolOnce(t, "claude", payload)
		if got := nested(t, raw, "raw")["claude_interrupted"]; got != true {
			t.Fatalf("raw.claude_interrupted = %v, want true", got)
		}
	})
}

// Claude Code's OTLP tool_result for the same call carries no output. The collector writes it
// without a working directory, so its dedupe key differs from the hook's and the hook's output
// reaches the log even when the collector's report is written first.
func TestClaudeBashOutputSurvivesAnEarlierCollectorReport(t *testing.T) {
	payload := postToolPayload("Bash", map[string]interface{}{"command": "npm test"},
		map[string]interface{}{"stdout": "ok\n", "stderr": "", "interrupted": false})
	otlp := claudeCommandLine(t, map[string]interface{}{
		"harness": map[string]interface{}{"name": "claude_code", "collection_method": "otlp"},
		"session": map[string]interface{}{"id": "s-743"},
		"command": map[string]interface{}{"command": "npm test", "duration_ms": 24},
	})
	_, events := runClaudePostTool(t, payload, otlp)
	if len(events) != 2 {
		t.Fatalf("events = %d, want the collector's report and the hook's", len(events))
	}
	if got := nested(t, events[1], "command")["output"]; got != "ok\n" {
		t.Fatalf("hook command.output = %v, want the output kept", got)
	}
}

// The transcript copy of a failed command is command.executed with the same call id and output. The
// hook's copy matches it, so `claude sync` does not store the output a second time.
func TestClaudeBashFailureMatchesTheTranscriptCopy(t *testing.T) {
	logPath, _ := runClaudePostTool(t, claudeBashFailurePayload("Exit code 1\nError: Cannot find module 'express'"))
	poll := claudeCommandLine(t, map[string]interface{}{
		"severity":   "high",
		"harness":    map[string]interface{}{"name": "claude_code", "collection_method": "poll"},
		"session":    map[string]interface{}{"id": "s-743", "working_directory": "/repo"},
		"repository": "/repo",
		"command":    map[string]interface{}{"command": "npm test", "output": "Exit code 1\nError: Cannot find module 'express'"},
	})
	if !asymptoteobserve.IsDuplicateEndpointEvent(logPath, []byte(poll), asymptoteobserve.EndpointDuplicateWindow) {
		t.Fatal("the transcript copy of a failed command should be suppressed by the hook's")
	}
}
