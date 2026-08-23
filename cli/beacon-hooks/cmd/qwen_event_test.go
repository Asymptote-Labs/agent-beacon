package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readQwenFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "qwen", name))
	if err != nil {
		t.Fatalf("read qwen fixture %s: %v", name, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode qwen fixture %s: %v", name, err)
	}
	return payload
}

// setupQwenHook wires a hook run for --platform qwen against a fresh runtime log.
func setupQwenHook(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "qwen"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	// Local git enrichment would shell out to whatever repository the test binary happens to run
	// in, which makes the branch field depend on the checkout rather than the payload.
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	return logPath
}

func qwenAction(t *testing.T, event map[string]interface{}) string {
	t.Helper()
	meta, ok := event["event"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no event object: %#v", event)
	}
	action, _ := meta["action"].(string)
	return action
}

// The whole point of the Qwen taxonomy: its built-ins are Gemini-era snake_case ids, and the
// generic substring classifier gets four of them wrong. Each of the wrong-before cases is listed
// here with the action it must produce, so a regression is a named failure rather than a silently
// reclassified event.
func TestQwenBuiltInToolsMapOntoTheRightAction(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "qwen"

	for toolName, want := range map[string]string{
		// Shell.
		"run_shell_command": "command.executed",
		// Filesystem reads. list_directory, glob and grep_search are the ones the generic
		// classifier drops to tool.invoked.
		"read_file":           "file.read",
		"read_many_files":     "file.read",
		"list_directory":      "file.read",
		"glob":                "file.read",
		"grep_search":         "file.read",
		"search_file_content": "file.read",
		// Filesystem writes. All four are tool.invoked under the default isFileEditTool, which
		// only knows Claude's Write/Edit/MultiEdit.
		"write_file":    "file.modified",
		"edit":          "file.modified",
		"replace":       "file.modified",
		"notebook_edit": "file.modified",
		// Not filesystem tools: these stay on the shared path deliberately.
		"web_fetch":   "tool.invoked",
		"web_search":  "tool.invoked",
		"todo_write":  "tool.invoked",
		"save_memory": "tool.invoked",
		"task":        "tool.invoked",
		"skill":       "tool.invoked",
	} {
		t.Run(toolName, func(t *testing.T) {
			if got := actionForTool("PostToolUse", toolName); got != want {
				t.Errorf("actionForTool(PostToolUse, %q) = %q, want %q", toolName, got, want)
			}
		})
	}
}

// An MCP tool is named by whoever wrote the server, so names containing "edit", "glob" or
// "read_file" as substrings are ordinary. The Qwen set is matched by equality precisely so those
// keep routing to mcp.tool_invoked instead of being claimed as Qwen built-ins.
func TestQwenTaxonomyDoesNotClaimMCPTools(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })
	platformFlag = "qwen"

	for _, toolName := range []string{
		"mcp__notion__edit", "mcp__fs__glob", "mcp__github__read_file", "mcp__shell__run_shell_command",
	} {
		if got := actionForTool("PostToolUse", toolName); got != "mcp.tool_invoked" {
			t.Errorf("actionForTool(%q) = %q, want mcp.tool_invoked", toolName, got)
		}
		if isFileEditTool("qwen", toolName) {
			t.Errorf("isFileEditTool(qwen, %q) = true; an MCP tool must not be sent through Qwen's diff path", toolName)
		}
	}
}

// The Qwen taxonomy must not leak into other runtimes. Its ids collide with real tools elsewhere --
// grok has its own write_file, and "edit" is a Devin tool -- so a mapping keyed on the platform is
// the only safe form.
func TestQwenTaxonomyIsScopedToTheQwenPlatform(t *testing.T) {
	origPlatform := platformFlag
	t.Cleanup(func() { platformFlag = origPlatform })

	platformFlag = "claude"
	if got := actionForTool("PostToolUse", "list_directory"); got != "tool.invoked" {
		t.Errorf("actionForTool(claude, list_directory) = %q, want the claude classification unchanged", got)
	}
	if isFileEditTool("claude", "replace") {
		t.Error("isFileEditTool(claude, replace) = true; Qwen's edit set must not apply to Claude Code")
	}
	if !isFileEditTool("claude", "Write") {
		t.Error("isFileEditTool(claude, Write) = false; Claude's own edit set regressed")
	}
}

// A tool that failed is recorded as a failure, not as the thing it was trying to do. Without the
// PostToolUseFailure check ordering ahead of the tool id, a write_file that hit EACCES would be
// written to the log as a successful file.modified.
func TestQwenFailedToolIsNotRecordedAsASuccessfulEdit(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_failure.json"))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed", got)
	}
	if got := event["severity"]; got != "high" {
		t.Fatalf("severity = %q, want high", got)
	}
	if got := event["harness"].(map[string]interface{})["name"]; got != "qwen_code" {
		t.Fatalf("harness.name = %q, want qwen_code", got)
	}
}

// An interrupt with an empty error and a PostToolUse hook event must still be recorded as a
// failure. Without this, the Qwen taxonomy maps write_file to file.modified even though the tool
// was interrupted and the write never landed.
func TestQwenInterruptedToolIsNotRecordedAsASuccessfulEdit(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_interrupt.json"))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "tool.failed" {
		t.Fatalf("event.action = %q, want tool.failed for an interrupted tool", got)
	}
	if got := event["severity"]; got != "high" {
		t.Fatalf("severity = %q, want high", got)
	}
}

// A shell command has to reach the `command` field, not just `tool.name`: every command rule,
// dashboard column and threat-rule match reads it from there.
func TestQwenShellCommandIsRecordedAsACommand(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_shell.json"))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	command, ok := event["command"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no command object: %#v", event)
	}
	if command["command"] != "npm test" {
		t.Fatalf("command.command = %q, want npm test", command["command"])
	}
	if got := event["tool"].(map[string]interface{})["name"]; got != "run_shell_command" {
		t.Fatalf("tool.name = %q, want run_shell_command", got)
	}
	if got := event["session"].(map[string]interface{})["working_directory"]; got != "/Users/example/projects/beacon-demo" {
		t.Fatalf("working_directory = %q, want the payload cwd", got)
	}
}

// A whole-file write records the path, the operation and the diff. The diff is what makes the event
// answer "what changed" rather than only "something changed".
func TestQwenWriteFileRecordsPathOperationAndDiff(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_write_file.json"))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	file, ok := event["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no file object: %#v", event)
	}
	if file["path"] != "/Users/example/projects/beacon-demo/src/health.ts" {
		t.Fatalf("file.path = %q, want the written path", file["path"])
	}
	// "modify" rather than "create", and that is the shared contract rather than a Qwen quirk: an
	// event that carries a diff is built by diffFields, which has no tool name to distinguish a
	// whole-file write from an in-place edit and reports "modify" for both on every runtime. The
	// create/modify distinction Qwen's taxonomy does make is carried by the pre-tool event, which
	// is asserted below.
	if file["operation"] != "modify" {
		t.Fatalf("file.operation = %q, want modify", file["operation"])
	}
	diff, _ := file["diff"].(string)
	if !strings.Contains(diff, "export const health") {
		t.Fatalf("file.diff = %q, want the written content", diff)
	}
}

// The create/modify distinction Qwen's taxonomy draws: `write_file` replaces a whole file and
// `edit` changes part of one. It reaches the log through the pre-tool event, which classifies from
// the tool name rather than from a constructed diff.
func TestQwenPreToolDistinguishesAWholeFileWriteFromAnEdit(t *testing.T) {
	for toolName, want := range map[string]string{"write_file": "create", "edit": "modify", "replace": "modify"} {
		t.Run(toolName, func(t *testing.T) {
			logPath := setupQwenHook(t)
			runHookWithInput(t, runPreTool, map[string]interface{}{
				"session_id":      "qwen-8f21c4a0",
				"cwd":             "/Users/example/projects/beacon-demo",
				"hook_event_name": "PreToolUse",
				"tool_name":       toolName,
				"tool_input": map[string]interface{}{
					"file_path": "/Users/example/projects/beacon-demo/src/health.ts",
				},
			})
			event := lastEndpointEvent(t, logPath)
			file, ok := event["file"].(map[string]interface{})
			if !ok {
				t.Fatalf("event has no file object: %#v", event)
			}
			if file["operation"] != want {
				t.Fatalf("file.operation = %q, want %q", file["operation"], want)
			}
		})
	}
}

// The `edit` tool's old_string/new_string shape has to reach the diff builder. Without `replace`
// and `edit` recognized as Qwen edit tools, the event says a file changed and never says how.
func TestQwenEditRecordsBothSidesOfTheReplacement(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_edit.json"))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	file := event["file"].(map[string]interface{})
	if file["path"] != "/Users/example/projects/beacon-demo/src/server.ts" {
		t.Fatalf("file.path = %q, want the edited path", file["path"])
	}
	diff, _ := file["diff"].(string)
	if !strings.Contains(diff, "const routes = []") || !strings.Contains(diff, "const routes = [health]") {
		t.Fatalf("file.diff = %q, want both sides of the replacement", diff)
	}
}

// Qwen's Gemini-era `replace` id carries the same payload as `edit`. Both must classify and diff
// identically, because which one arrives depends on the installed Qwen Code version rather than on
// what the agent did.
func TestQwenLegacyReplaceIdBehavesLikeEdit(t *testing.T) {
	logPath := setupQwenHook(t)

	payload := readQwenFixture(t, "post_tool_edit.json")
	payload["tool_name"] = "replace"
	runHookWithInput(t, runPostTool, payload)

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	file := event["file"].(map[string]interface{})
	// The generic fileOperation reads "replace" as neither a read nor a write and leaves this
	// empty; qwenFileOperation is what fills it.
	if file["operation"] != "modify" {
		t.Fatalf("file.operation = %q, want modify", file["operation"])
	}
	if diff, _ := file["diff"].(string); !strings.Contains(diff, "const routes = [health]") {
		t.Fatalf("file.diff = %q, want the replacement recorded", diff)
	}
}

// Gemini CLI's read_file names its target `absolute_path`. The list already carried the CamelCase
// `AbsolutePath`; without the snake_case sibling a Qwen file read produces an event with no path.
func TestQwenReadFileResolvesTheAbsolutePathParameter(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "qwen-8f21c4a0",
		"cwd":             "/Users/example/projects/beacon-demo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "read_file",
		"tool_input": map[string]interface{}{
			"absolute_path": "/Users/example/projects/beacon-demo/src/server.ts",
		},
	})

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.read" {
		t.Fatalf("event.action = %q, want file.read", got)
	}
	file, ok := event["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no file object: %#v", event)
	}
	if file["path"] != "/Users/example/projects/beacon-demo/src/server.ts" {
		t.Fatalf("file.path = %q, want the absolute_path value", file["path"])
	}
	if file["operation"] != "read" {
		t.Fatalf("file.operation = %q, want read", file["operation"])
	}
}

// Qwen carries fields the endpoint schema has nowhere to put -- permission_mode, tool_use_id,
// source, and the Stop context trio. raw.qwen is where they survive, and it goes through the same
// sanitizer as every other field.
func TestQwenRawPayloadPreservesFieldsWithNoSchemaHome(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPreTool, readQwenFixture(t, "pre_tool_shell.json"))

	event := lastEndpointEvent(t, logPath)
	raw, ok := event["raw"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no raw object: %#v", event)
	}
	payload, ok := raw["qwen"].(map[string]interface{})
	if !ok {
		t.Fatalf("raw has no qwen payload: %#v", raw)
	}
	for _, key := range []string{"permission_mode", "tool_use_id", "tool_call_id", "hook_event_name"} {
		if payload[key] == nil || payload[key] == "" {
			t.Errorf("raw.qwen is missing %s: %#v", key, payload)
		}
	}
}

// Stop's context_usage / context_limit / input_tokens are deliberately NOT normalized into
// gen_ai.usage.
//
// Beacon's token rollups sum gen_ai.usage across events. Qwen's Stop hook fires once per assistant
// turn and its input_tokens is the prompt token count for that turn's request -- which, in a
// multi-turn session, already contains every prior turn. Summing it would inflate a session's token
// total by roughly the square of its length. Qwen's own documentation is explicit that the value
// "may include output tokens depending on provider", so it is not a clean input count either.
//
// The three fields are preserved verbatim under raw.qwen, where they are exact and carry no claim
// of being a normalized token count. This test is the record of that decision: if a future change
// starts writing gen_ai.usage from a Qwen Stop payload, it fails here first.
//
// The event is built through emitHookEvent with the arguments runStop passes it, rather than by
// running the command. runStop ends the session-bearing path with outputJSONAndExit, and os.Exit in
// an in-process test aborts the run -- so the command itself is not callable from this harness,
// while the mapping it performs is exactly what this asserts.
func TestQwenStopContextFieldsAreNotNormalizedIntoTokenUsage(t *testing.T) {
	logPath := setupQwenHook(t)

	input := readQwenFixture(t, "stop.json")
	sessionID, _ := resolveSessionIDWithTranscript(input, platformFlag)
	if sessionID == "" {
		t.Fatal("stop fixture has no session id; the mapping under test would not run")
	}
	logger := newHookLogger("stop", platformFlag, sessionID)
	emitHookEvent(logger, "tool.completed", "tool", "info", "Agent response completed", input, sessionFields(sessionID, input))

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "tool.completed" {
		t.Fatalf("event.action = %q, want tool.completed", got)
	}
	if genAI, ok := event["gen_ai"].(map[string]interface{}); ok {
		if usage, ok := genAI["usage"]; ok {
			t.Fatalf("gen_ai.usage = %#v; Qwen's per-turn prompt token count must not be summed as usage", usage)
		}
	}
	payload, ok := event["raw"].(map[string]interface{})["qwen"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no raw.qwen payload: %#v", event["raw"])
	}
	for _, key := range []string{"context_usage", "context_limit", "input_tokens", "stop_hook_active"} {
		if payload[key] == nil {
			t.Errorf("raw.qwen is missing %s, which is the only place it is preserved: %#v", key, payload)
		}
	}
}

// The lifecycle events a session is reconstructed from. Prompt text and the session model are what
// make a Qwen session legible in the dashboard rather than a list of anonymous tool calls.
func TestQwenSessionLifecycleIsRecorded(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runSessionStart, readQwenFixture(t, "session_start.json"))
	start := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, start); got != "session.started" {
		t.Fatalf("event.action = %q, want session.started", got)
	}
	if got := start["model"]; got != "qwen3-coder-plus" {
		t.Fatalf("model = %q, want the session model", got)
	}

	runHookWithInput(t, runPromptSubmit, readQwenFixture(t, "user_prompt_submit.json"))
	prompt := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, prompt); got != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", got)
	}
	if got := prompt["prompt"].(map[string]interface{})["text"]; got != "Add a health endpoint, then run the tests" {
		t.Fatalf("prompt.text = %q, want the submitted prompt", got)
	}

	runHookWithInput(t, runSessionEnd, map[string]interface{}{
		"session_id":      "qwen-8f21c4a0",
		"cwd":             "/Users/example/projects/beacon-demo",
		"hook_event_name": "SessionEnd",
		"reason":          "prompt_input_exit",
	})
	for _, event := range endpointEvents(t, logPath) {
		if qwenAction(t, event) == "session.ended" {
			return
		}
	}
	// session-end is allowed to be log-only on platforms that do not emit an endpoint event for
	// it; what must not happen is the run failing or writing a mis-typed event.
	if got := qwenAction(t, lastEndpointEvent(t, logPath)); got != "prompt.submitted" {
		t.Fatalf("last event after session-end = %q, want the log left intact", got)
	}
}

// The diff path writes its event directly rather than through emitHookEvent, so the raw payload
// that function attaches does not reach it.
//
// That gap lands on exactly the events this runtime's taxonomy exists to produce. A successful
// `write_file` or `edit` is classified `file.modified` and routed through recordLocalEdit, so
// without an explicit attachment there, `permission_mode` and `tool_use_id` -- which have no schema
// field and whose only home is raw.qwen -- survive on failed and non-edit tools and vanish on the
// successful edits. TestQwenRawPayloadPreservesFieldsWithNoSchemaHome does not catch it because it
// exercises the pre-tool path, which does go through emitHookEvent.
func TestQwenRawPayloadSurvivesTheFileEditPath(t *testing.T) {
	logPath := setupQwenHook(t)

	for _, fixture := range []string{"post_tool_write_file.json", "post_tool_edit.json"} {
		t.Run(fixture, func(t *testing.T) {
			runHookWithInput(t, runPostTool, readQwenFixture(t, fixture))
			event := lastEndpointEvent(t, logPath)
			if got := qwenAction(t, event); got != "file.modified" {
				t.Fatalf("event.action = %q, want file.modified", got)
			}
			raw, ok := event["raw"].(map[string]interface{})
			if !ok {
				t.Fatalf("file.modified event has no raw object: %#v", event)
			}
			payload, ok := raw["qwen"].(map[string]interface{})
			if !ok {
				t.Fatalf("file.modified event has no raw.qwen payload: %#v", raw)
			}
			for _, key := range []string{"permission_mode", "tool_use_id", "hook_event_name"} {
				if payload[key] == nil || payload[key] == "" {
					t.Errorf("raw.qwen on a file edit is missing %s: %#v", key, payload)
				}
			}
		})
	}
}

// The two paths that ask "did this tool fail" must never disagree.
//
// They did once: `qwenToolFailed` guards the diff path and reads three signals, while the
// classification in emitPostToolObserved read only two. An interrupt with an empty error skipped
// the diff path as a failure and was then classified a successful `file.modified` -- the exact
// false positive this runtime's mapping exists to prevent, reintroduced by the gap between two
// spellings of one question.
//
// Every signal is exercised against the same edit tool, because an edit tool is where the
// disagreement is expensive: a misclassified `web_fetch` is noise, a misclassified `write_file`
// asserts a file changed when it did not.
func TestQwenEveryFailureSignalIsClassifiedAsAFailure(t *testing.T) {
	for name, overrides := range map[string]map[string]interface{}{
		"failure event name": {"hook_event_name": "PostToolUseFailure"},
		"error message":      {"hook_event_name": "PostToolUse", "error": "EACCES: permission denied"},
		"interrupt flag":     {"hook_event_name": "PostToolUse", "error": "", "is_interrupt": true},
	} {
		t.Run(name, func(t *testing.T) {
			logPath := setupQwenHook(t)
			payload := readQwenFixture(t, "post_tool_write_file.json")
			for key, value := range overrides {
				payload[key] = value
			}

			runHookWithInput(t, runPostTool, payload)

			event := lastEndpointEvent(t, logPath)
			if got := qwenAction(t, event); got != "tool.failed" {
				t.Fatalf("event.action = %q, want tool.failed", got)
			}
			if got := event["severity"]; got != "high" {
				t.Errorf("severity = %q, want high", got)
			}
			// A failure never carries a diff. The content in the payload describes a write that
			// did not land, so recording it would assert a change that never happened.
			if file, ok := event["file"].(map[string]interface{}); ok {
				if diff, ok := file["diff"]; ok {
					t.Errorf("failed edit carried a diff: %v", diff)
				}
			}
		})
	}

	// The control: the same payload with no failure signal is still a successful edit, so the
	// guard above cannot be satisfied by classifying everything as a failure.
	logPath := setupQwenHook(t)
	runHookWithInput(t, runPostTool, readQwenFixture(t, "post_tool_write_file.json"))
	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.modified" {
		t.Fatalf("a clean write was classified %q, want file.modified", got)
	}
}

// What a Qwen `notebook_edit` actually records, pinned so the answer is written down rather than
// rediscovered.
//
// It is classified `file.modified` with the notebook's path and a `modify` operation, and it carries
// no diff. The missing diff is not the path resolver: all three readers now accept `notebook_path`.
// It is `IsScannableFile`, which does not list `.ipynb`, so the diff path returns before a diff is
// attempted -- and `notebook_edit`'s payload carries `cell_id` / `new_source` rather than the
// `old_string` / `new_string` pair the diff builder knows, so there would be nothing to build from
// without a `structuredPatch`.
//
// Recording the path and the action while honestly carrying no diff is the right outcome: the event
// says a notebook changed, and does not pretend to say how.
func TestQwenNotebookEditRecordsThePathWithoutClaimingADiff(t *testing.T) {
	logPath := setupQwenHook(t)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "qwen-8f21c4a0",
		"cwd":             "/Users/example/projects/beacon-demo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "notebook_edit",
		"tool_input": map[string]interface{}{
			"notebook_path": "/Users/example/projects/beacon-demo/analysis.ipynb",
			"cell_id":       "cell-1",
			"new_source":    "print('hello')",
			"edit_mode":     "replace",
		},
		"tool_response": map[string]interface{}{"success": true},
	})

	event := lastEndpointEvent(t, logPath)
	if got := qwenAction(t, event); got != "file.modified" {
		t.Fatalf("event.action = %q, want file.modified", got)
	}
	file, ok := event["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("event has no file object: %#v", event)
	}
	if file["path"] != "/Users/example/projects/beacon-demo/analysis.ipynb" {
		t.Fatalf("file.path = %q, want the notebook_path value", file["path"])
	}
	if file["operation"] != "modify" {
		t.Fatalf("file.operation = %q, want modify", file["operation"])
	}
	if diff, ok := file["diff"]; ok {
		t.Fatalf("file.diff = %v; a notebook edit has no diff to build, so claiming one would be "+
			"asserting content Beacon never saw", diff)
	}
}
