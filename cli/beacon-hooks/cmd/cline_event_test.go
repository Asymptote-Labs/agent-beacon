package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clineTestLog puts a Cline hook run in a temp endpoint log and returns its path.
func clineTestLog(t *testing.T) string {
	t.Helper()
	setupHookConfigDirs(t)
	platformFlag = "cline"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")
	return logPath
}

func clineEventActions(t *testing.T, logPath string) []string {
	t.Helper()
	var actions []string
	for _, event := range endpointEvents(t, logPath) {
		event, _ := event["event"].(map[string]interface{})
		actions = append(actions, event["action"].(string))
	}
	return actions
}

func clineEventWithAction(t *testing.T, logPath, action string) map[string]interface{} {
	t.Helper()
	for _, event := range endpointEvents(t, logPath) {
		meta, _ := event["event"].(map[string]interface{})
		if meta["action"] == action {
			return event
		}
	}
	t.Fatalf("no %s event in log; got %v", action, clineEventActions(t, logPath))
	return nil
}

func readClineFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "cline", name))
	if err != nil {
		t.Fatalf("read cline fixture %s: %v", name, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode cline fixture %s: %v", name, err)
	}
	return payload
}

func nested(t *testing.T, event map[string]interface{}, keys ...string) map[string]interface{} {
	t.Helper()
	current := event
	for _, key := range keys {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			t.Fatalf("event is missing the %s object: %v", strings.Join(keys, "."), event)
		}
		current = next
	}
	return current
}

// A task start is both the start of a session and the moment the user's request arrives, so it
// produces two events. Recording only one would lose either the session boundary or the prompt.
func TestClineEventTaskStartRecordsSessionAndPrompt(t *testing.T) {
	logPath := clineTestLog(t)

	out := runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":           "beforeRun",
		"taskId":         "task-1",
		"workspaceRoots": []interface{}{"/tmp/project"},
		"apiProvider":    "anthropic",
		"modelId":        "claude-sonnet-4",
		"input":          map[string]interface{}{"text": "ship it token=cline-secret"},
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}

	if got := clineEventActions(t, logPath); len(got) != 2 || got[0] != "session.started" || got[1] != "prompt.submitted" {
		t.Fatalf("actions = %v, want [session.started prompt.submitted]", got)
	}
	event := clineEventWithAction(t, logPath, "prompt.submitted")
	if harness := nested(t, event, "harness")["name"]; harness != "cline" {
		t.Errorf("harness.name = %q, want cline", harness)
	}
	if id := nested(t, event, "session")["id"]; id != "task-1" {
		t.Errorf("session.id = %q, want task-1", id)
	}
	if got := nested(t, event, "prompt")["text"]; got != "ship it token=[REDACTED]" {
		t.Errorf("prompt.text = %q, want the redacted prompt", got)
	}
	if got := event["model"]; got != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q, want the provider-qualified model", got)
	}
	if got := nested(t, event, "session")["working_directory"]; got != "/tmp/project" {
		t.Errorf("session.working_directory = %q, want the workspace root", got)
	}
}

// Base fields resolve the workspace through the Cline-specific path (workspaceRoots) regardless of
// what platformFlag is set to. Without this, the session.working_directory stays empty when the
// binary is invoked without --platform cline.
func TestClineEventBaseFieldsResolveWorkspaceWithoutPlatformFlag(t *testing.T) {
	logPath := clineTestLog(t)
	platformFlag = "claude" // simulate missing --platform cline

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":           "beforeRun",
		"taskId":         "task-platform",
		"workspaceRoots": []interface{}{"/tmp/project"},
		"input":          map[string]interface{}{"text": "hello"},
	})

	event := clineEventWithAction(t, logPath, "session.started")
	if got := nested(t, event, "session")["working_directory"]; got != "/tmp/project" {
		t.Errorf("session.working_directory = %q, want /tmp/project (resolved from workspaceRoots regardless of platformFlag)", got)
	}
}

// The file-based hook surface names the hook in `hookName` and sends prompts on their own, so a
// prompt payload with no run-start context still has to produce a prompt event.
func TestClineEventPromptOnlyPayload(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"hookName": "UserPromptSubmit",
		"taskId":   "task-2",
		"prompt":   "explain this repo",
	})

	if got := clineEventActions(t, logPath); len(got) != 1 || got[0] != "prompt.submitted" {
		t.Fatalf("actions = %v, want [prompt.submitted]", got)
	}
}

func TestClineEventBeforeToolRecordsInvocation(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":           "beforeTool",
		"taskId":         "task-3",
		"workspaceRoots": []interface{}{"/tmp/project"},
		"toolCall":       map[string]interface{}{"name": "read_file", "input": map[string]interface{}{"path": "src/app.ts"}},
	})

	event := clineEventWithAction(t, logPath, "tool.invoked")
	if got := nested(t, event, "tool")["name"]; got != "read_file" {
		t.Errorf("tool.name = %q, want read_file", got)
	}
	if got := nested(t, event, "gen_ai", "tool", "call")["arguments"]; got == nil {
		t.Errorf("gen_ai.tool.call.arguments is missing: %v", event)
	}
}

// Cline addresses files relative to the workspace. Every other runtime reports absolute paths, so
// the join is what makes a Cline file event comparable with the same file seen through Cursor or
// Claude Code -- and what lets a threat rule matching an absolute path fire on Cline activity.
func TestClineEventResolvesWorkspaceRelativePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roots interface{}
		path  string
		want  string
	}{
		{
			// A literal expectation, not filepath.Join: the result must be the same on every host,
			// because the workspace the path belongs to is not necessarily the host's.
			name:  "relative path is joined to the workspace root",
			roots: []interface{}{"/tmp/project"},
			path:  "src/app.ts",
			want:  "/tmp/project/src/app.ts",
		},
		{
			// filepath.IsAbs says false for this on Windows, so the old implementation joined it and
			// recorded \tmp\project\etc\hosts -- a file nothing touched.
			name:  "absolute POSIX path is left alone",
			roots: []interface{}{"/tmp/project"},
			path:  "/etc/hosts",
			want:  "/etc/hosts",
		},
		{
			name:  "relative path stays relative when no root is known",
			roots: nil,
			path:  "src/app.ts",
			want:  "src/app.ts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := clineTestLog(t)
			payload := map[string]interface{}{
				"type":     "afterTool",
				"taskId":   "task-4",
				"toolCall": map[string]interface{}{"name": "read_file", "input": map[string]interface{}{"path": tc.path}},
			}
			if tc.roots != nil {
				payload["workspaceRoots"] = tc.roots
			}
			runHookWithInput(t, runClineEvent, payload)

			event := clineEventWithAction(t, logPath, "file.read")
			if got := nested(t, event, "file")["path"]; got != tc.want {
				t.Errorf("file.path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClineEventWriteToFileRecordsDiff(t *testing.T) {
	logPath := clineTestLog(t)
	runHookWithInput(t, runClineEvent, readClineFixture(t, "tool_after_write.json"))

	event := clineEventWithAction(t, logPath, "file.modified")
	file := nested(t, event, "file")
	// Literal, not filepath.Join: the fixture's workspace is a POSIX one, and the recorded path must
	// keep that shape whichever host the hook binary runs on.
	if got := file["path"]; got != "/Users/example/projects/beacon-demo/src/health.ts" {
		t.Errorf("file.path = %q, want the resolved workspace path", got)
	}
	// write_to_file creates a file. diffFields records every diff as a modification, so a create
	// that came back as "modify" would mean the operation was overwritten by the diff builder.
	if got := file["operation"]; got != "create" {
		t.Errorf("file.operation = %q, want create", got)
	}
	if file["diff_hash"] == nil || file["diff_bytes"] == nil {
		t.Errorf("file diff metadata is missing: %v", file)
	}
	// The content Cline wrote is recorded as an added-lines diff through the shared builder, which
	// names files by base name for every runtime. The absolute path lives in file.path above.
	diff, _ := file["diff"].(string)
	if !strings.Contains(diff, "+++ b/health.ts") || !strings.Contains(diff, "+export const health") {
		t.Errorf("diff does not describe the written file: %q", diff)
	}
	if got := nested(t, event, "tool")["duration_ms"]; got != float64(34) {
		t.Errorf("tool.duration_ms = %v, want 34", got)
	}
}

// replace_in_file's argument is already a diff, so it is passed through rather than reconstructed:
// what Beacon records should be what Cline applied.
func TestClineEventReplaceInFilePassesTheDiffThrough(t *testing.T) {
	logPath := clineTestLog(t)
	diffText := "------- SEARCH\nold()\n=======\nnew()\n+++++++ REPLACE"

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":           "afterTool",
		"taskId":         "task-5",
		"workspaceRoots": []interface{}{"/tmp/project"},
		"toolCall": map[string]interface{}{
			"name":  "replace_in_file",
			"input": map[string]interface{}{"path": "src/app.ts", "diff": diffText},
		},
	})

	file := nested(t, clineEventWithAction(t, logPath, "file.modified"), "file")
	if got := file["operation"]; got != "modify" {
		t.Errorf("file.operation = %q, want modify", got)
	}
	if got := file["diff"]; got != diffText {
		t.Errorf("file.diff = %q, want the diff Cline applied", got)
	}
	if got := file["diff_bytes"]; got != float64(len(diffText)) {
		t.Errorf("file.diff_bytes = %v, want %d", got, len(diffText))
	}
}

func TestClineEventExecuteCommandRecordsCommand(t *testing.T) {
	logPath := clineTestLog(t)
	runHookWithInput(t, runClineEvent, readClineFixture(t, "tool_after_command.json"))

	event := clineEventWithAction(t, logPath, "command.executed")
	command := nested(t, event, "command")
	if got := command["command"]; got != "npm test" {
		t.Errorf("command.command = %q, want npm test", got)
	}
	if got := command["output"]; got != "2 passing" {
		t.Errorf("command.output = %q, want the captured output", got)
	}
	// A zero exit code is a real value, not an absent one: dropping it would report every clean
	// command as having no result.
	if got, ok := command["exit_code"]; !ok || got != float64(0) {
		t.Errorf("command.exit_code = %v (present=%t), want 0", got, ok)
	}
	if got := command["duration_ms"]; got != float64(4120) {
		t.Errorf("command.duration_ms = %v, want 4120", got)
	}
}

func TestClineEventMCPToolIsRecordedAsMCP(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":   "afterTool",
		"taskId": "task-6",
		"toolCall": map[string]interface{}{
			"name":  "use_mcp_tool",
			"input": map[string]interface{}{"server_name": "github", "tool_name": "list_issues"},
		},
	})

	if got := clineEventActions(t, logPath); len(got) != 1 || got[0] != "mcp.tool_invoked" {
		t.Fatalf("actions = %v, want [mcp.tool_invoked]", got)
	}
}

func TestClineEventToolFailureIsHighSeverity(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":     "afterTool",
		"taskId":   "task-7",
		"toolCall": map[string]interface{}{"name": "read_file", "input": map[string]interface{}{"path": "missing.ts"}},
		"error":    map[string]interface{}{"message": "ENOENT: no such file"},
	})

	event := clineEventWithAction(t, logPath, "tool.failed")
	if got := event["severity"]; got != "high" {
		t.Errorf("severity = %q, want high", got)
	}
	if got := nested(t, event, "error")["type"]; got != "tool_error" {
		t.Errorf("error.type = %q, want tool_error", got)
	}
}

// A successful tool result that carries a "message" field (common in Cline for status feedback)
// must not be treated as a failure. Only explicit error keys indicate a failure.
func TestClineEventSuccessfulToolWithMessageIsNotFailure(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":   "afterTool",
		"taskId": "task-msg",
		"toolCall": map[string]interface{}{
			"name":  "write_to_file",
			"input": map[string]interface{}{"path": "out.ts", "content": "done"},
		},
		"result":         map[string]interface{}{"output": "File written", "message": "File written successfully"},
		"workspaceRoots": []interface{}{"/tmp/project"},
	})

	actions := clineEventActions(t, logPath)
	for _, a := range actions {
		if a == "tool.failed" {
			t.Fatalf("actions = %v, want no tool.failed for a successful result with a message field", actions)
		}
	}
	if len(actions) == 0 {
		t.Fatalf("expected at least one event")
	}
}

// A file action with no file is not a file action: Cline's search tools take a pattern rather than
// a path, and a file.read with no file field matches every file-scoped query and explains none.
func TestClineEventFileActionWithoutAPathBecomesToolCompleted(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":     "afterTool",
		"taskId":   "task-8",
		"toolCall": map[string]interface{}{"name": "search_files", "input": map[string]interface{}{"regex": "TODO"}},
	})

	if got := clineEventActions(t, logPath); len(got) != 1 || got[0] != "tool.completed" {
		t.Fatalf("actions = %v, want [tool.completed]", got)
	}
}

func TestClineEventTaskEndRecordsUsage(t *testing.T) {
	logPath := clineTestLog(t)
	runHookWithInput(t, runClineEvent, readClineFixture(t, "run_end_usage.json"))

	event := clineEventWithAction(t, logPath, "session.ended")
	usage := nested(t, event, "gen_ai", "usage")
	if got := usage["input_tokens"]; got != float64(1841) {
		t.Errorf("gen_ai.usage.input_tokens = %v, want 1841", got)
	}
	if got := usage["output_tokens"]; got != float64(412) {
		t.Errorf("gen_ai.usage.output_tokens = %v, want 412", got)
	}
	if got := nested(t, event, "gen_ai", "usage", "cache_read")["input_tokens"]; got != float64(1024) {
		t.Errorf("gen_ai.usage.cache_read.input_tokens = %v, want 1024", got)
	}
	if got := usage["cost_usd"]; got != 0.0231 {
		t.Errorf("gen_ai.usage.cost_usd = %v, want the runtime-reported cost", got)
	}
}

// Cost is only ever what the runtime reported. Beacon must not derive it from a local price table,
// so a usage payload without a cost must produce no cost field at all.
func TestClineEventDoesNotInventCost(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":   "afterRun",
		"taskId": "task-9",
		"result": map[string]interface{}{"usage": map[string]interface{}{"inputTokens": 10, "outputTokens": 2}},
	})

	usage := nested(t, clineEventWithAction(t, logPath, "session.ended"), "gen_ai", "usage")
	if _, ok := usage["cost_usd"]; ok {
		t.Errorf("gen_ai.usage.cost_usd = %v, want no cost when the runtime reported none", usage["cost_usd"])
	}
}

// Token usage is read at exactly one lifecycle point. Beacon's rollups sum gen_ai.usage across
// events, so a second stage reporting the same tokens would double every Cline task's token count.
func TestClineEventReportsUsageOnlyAtTaskEnd(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":     "afterModel",
		"taskId":   "task-10",
		"usage":    map[string]interface{}{"inputTokens": 10, "outputTokens": 2},
		"toolCall": map[string]interface{}{"name": "read_file"},
	})

	if data, err := os.ReadFile(logPath); err == nil && len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("afterModel wrote events, want none: %s", data)
	}
}

// An unrecognized stage writes nothing. Cline streams far more runtime events than the lifecycle
// points Beacon maps, and a generic "something happened" row per event would bury the log.
func TestClineEventIgnoresUnknownStages(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"type":   "iteration_start",
		"taskId": "task-11",
	})

	if data, err := os.ReadFile(logPath); err == nil && len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("unknown stage wrote events, want none: %s", data)
	}
}

// A malformed payload must not fail the hook. Cline runs these in-process; an error here would
// surface in the user's editor, and telemetry is never worth that.
func TestClineEventSurvivesAnEmptyPayload(t *testing.T) {
	clineTestLog(t)
	out := runHookWithInput(t, runClineEvent, map[string]interface{}{})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
}

// Every spelling the mapper claims to accept must still produce an event. These strings are the
// contract between this mapper and the managed plugin, and a typo on either side produces silence
// rather than an error.
func TestClineEventSupportedTypesAllProduceEvents(t *testing.T) {
	for _, eventType := range supportedClineEventTypes() {
		t.Run(eventType, func(t *testing.T) {
			logPath := clineTestLog(t)
			runHookWithInput(t, runClineEvent, map[string]interface{}{
				"type":           eventType,
				"taskId":         "task-12",
				"workspaceRoots": []interface{}{"/tmp/project"},
				"prompt":         "do the thing",
				"toolCall":       map[string]interface{}{"name": "read_file", "input": map[string]interface{}{"path": "src/app.ts"}},
				"usage":          map[string]interface{}{"inputTokens": 4},
				"error":          map[string]interface{}{"name": "aborted"},
			})
			if actions := clineEventActions(t, logPath); len(actions) == 0 {
				t.Fatalf("type %q produced no events", eventType)
			}
		})
	}
}

func TestClineStageRecognizesSpellingVariants(t *testing.T) {
	for input, want := range map[string]string{
		"beforeRun":        clineStageTaskStart,
		"run_start":        clineStageTaskStart,
		"TaskStart":        clineStageTaskStart,
		"pre_tool_use":     clineStageToolBefore,
		"PreToolUse":       clineStageToolBefore,
		"tool_call_before": clineStageToolBefore,
		"PostToolUse":      clineStageToolAfter,
		"tool_call_after":  clineStageToolAfter,
		"afterRun":         clineStageTaskEnd,
		"session_shutdown": clineStageTaskEnd,
		// A cancel and a failure are different outcomes and must not share a stage.
		"TaskCancel":      clineStageTaskCancel,
		"task_cancel":     clineStageTaskCancel,
		"stop_error":      clineStageTaskError,
		"error":           clineStageTaskError,
		"iteration_start": "",
		"":                "",
	} {
		t.Run(input, func(t *testing.T) {
			if got := clineStage(map[string]interface{}{"type": input}); got != want {
				t.Errorf("clineStage(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestClineToolActionClassification(t *testing.T) {
	for tool, want := range map[string]string{
		"read_file":                  "file.read",
		"list_files":                 "file.read",
		"search_files":               "file.read",
		"list_code_definition_names": "file.read",
		"write_to_file":              "file.modified",
		"replace_in_file":            "file.modified",
		"execute_command":            "command.executed",
		"run_commands":               "command.executed",
		"use_mcp_tool":               "mcp.tool_invoked",
		"access_mcp_resource":        "mcp.tool_invoked",
		"browser_action":             "tool.completed",
		"ask_followup_question":      "tool.completed",
	} {
		t.Run(tool, func(t *testing.T) {
			if got, _ := clineToolAction(tool); got != want {
				t.Errorf("clineToolAction(%q) = %q, want %q", tool, got, want)
			}
		})
	}
}

// The token rules must not classify a tool by a word that merely appears inside another word --
// the reason toolNameHasToken splits on boundaries instead of matching substrings.
func TestClineToolActionDoesNotMatchSubstrings(t *testing.T) {
	for _, tool := range []string{"spreadsheet_export", "thread_summary"} {
		if got, _ := clineToolAction(tool); got != "tool.completed" {
			t.Errorf("clineToolAction(%q) = %q, want tool.completed", tool, got)
		}
	}
}

// The workspace a Cline path belongs to is not necessarily the host Beacon runs on -- VS Code Remote
// and WSL are the ordinary cases -- so this resolution answers for the path, not for the host, and
// every row below holds identically on Linux, macOS, and Windows.
func TestClineWorkspacePathIsHostIndependent(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		root string
		want string
	}{
		{name: "posix relative under posix root", path: "src/app.ts", root: "/repo", want: "/repo/src/app.ts"},
		{name: "windows relative under windows root", path: `src\app.ts`, root: `C:\repo`, want: `C:\repo\src\app.ts`},
		// Mixed spellings happen: the payload and the workspace root do not have to agree, and the
		// recorded path should match the root's shape rather than becoming a mixture.
		{name: "posix relative under windows root", path: "src/app.ts", root: `C:\repo`, want: `C:\repo\src\app.ts`},
		{name: "windows relative under posix root", path: `src\app.ts`, root: "/repo", want: "/repo/src/app.ts"},
		{name: "dot prefix is dropped", path: "./src/app.ts", root: "/repo", want: "/repo/src/app.ts"},
		{name: "trailing root separator is not doubled", path: "src/app.ts", root: "/repo/", want: "/repo/src/app.ts"},
		{name: "posix absolute is left alone", path: "/etc/hosts", root: `C:\repo`, want: "/etc/hosts"},
		{name: "drive absolute is left alone", path: `C:\Windows\hosts`, root: "/repo", want: `C:\Windows\hosts`},
		{name: "drive absolute with forward slashes is left alone", path: "D:/data/a.ts", root: "/repo", want: "D:/data/a.ts"},
		{name: "unc path is left alone", path: `\\server\share\a.ts`, root: "/repo", want: `\\server\share\a.ts`},
		{name: "volume-rooted path is left alone", path: `\Users\x\a.ts`, root: "/repo", want: `\Users\x\a.ts`},
		// Drive-relative, which names a location only in the context of that drive's working
		// directory. Not rooted, so it is resolved like any other relative path.
		{name: "drive relative is treated as relative", path: "C:src/app.ts", root: "/repo", want: "/repo/C:src/app.ts"},
		{name: "no root leaves the path relative", path: "src/app.ts", root: "", want: "src/app.ts"},
		{name: "empty path stays empty", path: "", root: "/repo", want: ""},
		{name: "quoted path is unquoted first", path: `"src/app.ts"`, root: "/repo", want: "/repo/src/app.ts"},
		// Traversal is the case detection cares about most: stored uncollapsed, "/repo/../../.ssh/id_rsa"
		// names the same file as "/home/u/.ssh/id_rsa" and matches no rule written against either.
		{name: "traversal is collapsed", path: "../sibling/a.ts", root: "/repo/pkg", want: "/repo/sibling/a.ts"},
		{name: "traversal above the root resolves lexically", path: "../../.ssh/id_rsa", root: "/repo", want: "/.ssh/id_rsa"},
		{name: "dot segments in the middle are collapsed", path: "src/./util/../app.ts", root: "/repo", want: "/repo/src/app.ts"},
		// A drive prefix cannot go through path.Clean: it treats "C:" as an ordinary segment that
		// ".." can walk past, which would turn a drive path into a relative one.
		{name: "traversal under a drive root keeps the drive", path: `..\other\a.ts`, root: `C:\repo\pkg`, want: `C:\repo\other\a.ts`},
		{name: "traversal above a drive root keeps the drive", path: `..\..\..\x.ts`, root: `C:\repo`, want: `C:\x.ts`},
		// A leading "//" is a share, not a doubled separator, and path.Clean would collapse it.
		{name: "unc share survives cleaning", path: `..\b.ts`, root: `\\server\share\repo\pkg`, want: `\\server\share\repo\b.ts`},
		// A bare volume leaves nothing after the prefix, and path.Join drops empty elements -- which
		// ran the share and the path together as "\\server\sharesrc\app.ts".
		{name: "bare unc share keeps its separator", path: "src/app.ts", root: `\\server\share`, want: `\\server\share\src\app.ts`},
		{name: "unc share with trailing separator", path: "src/app.ts", root: `\\server\share\`, want: `\\server\share\src\app.ts`},
		{name: "bare server keeps its separator", path: "src/app.ts", root: `\\server`, want: `\\server\src\app.ts`},
		// A workspace root of "C:" means that drive's root, not a path relative to its working
		// directory, so the result is rooted rather than the drive-relative "C:src\app.ts".
		{name: "bare drive root is rooted", path: "src/app.ts", root: "C:", want: `C:\src\app.ts`},
		{name: "forward slash unc share keeps its separator", path: "src/app.ts", root: "//server/share", want: "//server/share/src/app.ts"},

		{name: "dotdot segments are collapsed", path: "../../.ssh/id_rsa", root: "/home/u/proj", want: "/home/.ssh/id_rsa"},
		{name: "single dot segments are collapsed", path: "./src/./app.ts", root: "/repo", want: "/repo/src/app.ts"},
		{name: "mixed traversal is collapsed", path: "src/../lib/app.ts", root: "/repo", want: "/repo/lib/app.ts"},
		{name: "windows dotdot is collapsed", path: `..\..\secret.txt`, root: `C:\Users\dev\project`, want: `C:\Users\secret.txt`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clineWorkspacePath(tc.path, tc.root); got != tc.want {
				t.Errorf("clineWorkspacePath(%q, %q) = %q, want %q", tc.path, tc.root, got, tc.want)
			}
		})
	}
}

// An error object with no message-like field is still a failure. Treating its empty text as "no
// error" recorded a failed tool call as a successful one -- the quietest way to be wrong, because
// the event still writes and reads as ordinary activity.
func TestClineEventToolFailureWithoutAMessage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		errValue interface{}
		wantType string
	}{
		{name: "name only", errValue: map[string]interface{}{"name": "ENOENT"}, wantType: "ENOENT"},
		{name: "type and code", errValue: map[string]interface{}{"type": "spawn_failed", "code": float64(2)}, wantType: "spawn_failed"},
		{name: "numeric code only", errValue: map[string]interface{}{"code": float64(2)}, wantType: "tool_error"},
		{name: "empty object", errValue: map[string]interface{}{}, wantType: "tool_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := clineTestLog(t)
			runHookWithInput(t, runClineEvent, map[string]interface{}{
				"type":     "afterTool",
				"taskId":   "task-1",
				"toolCall": map[string]interface{}{"name": "read_file", "input": map[string]interface{}{"path": "missing.ts"}},
				"error":    tc.errValue,
			})

			event := clineEventWithAction(t, logPath, "tool.failed")
			if got := event["severity"]; got != "high" {
				t.Errorf("severity = %q, want high", got)
			}
			if got := nested(t, event, "error")["type"]; got != tc.wantType {
				t.Errorf("error.type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

// A cancel is a person stopping their own task, not an incident. Recorded as session.error at high
// severity it counted every deliberate stop as a failure, inflating error rollups and tripping
// alerts on the most ordinary thing anyone does with an agent.
func TestClineEventTaskCancelIsAnEndNotAnError(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"hookName": "TaskCancel",
		"taskId":   "task-cancelled",
		"usage":    map[string]interface{}{"inputTokens": 40, "outputTokens": 5},
	})

	event := clineEventWithAction(t, logPath, "session.ended")
	if got := event["severity"]; got != "info" {
		t.Errorf("severity = %q, want info", got)
	}
	if _, ok := event["error"]; ok {
		t.Errorf("cancel carried an error field: %v", event["error"])
	}
	// The reason field is what lets a rollup separate cancels from completions without parsing the
	// message text.
	if got := nested(t, event, "session")["cancel_reason"]; got != "cancelled" {
		t.Errorf("session.cancel_reason = %v, want cancelled", got)
	}
	// A cancelled task still reports what it spent.
	if got := nested(t, event, "gen_ai", "usage")["input_tokens"]; got != float64(40) {
		t.Errorf("gen_ai.usage.input_tokens = %v, want 40", got)
	}
}

// Cline's file-based hook puts completionStatus inside taskCancel.taskMetadata. When only
// that nested field says "abandoned", the cancel reason must reflect it rather than defaulting.
func TestClineEventTaskCancelReadsNestedCompletionStatus(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"hookName": "TaskCancel",
		"taskId":   "task-abandoned",
		"taskCancel": map[string]interface{}{
			"taskMetadata": map[string]interface{}{
				"completionStatus": "abandoned",
			},
		},
	})

	event := clineEventWithAction(t, logPath, "session.ended")
	if got := nested(t, event, "session")["cancel_reason"]; got != "abandoned" {
		t.Errorf("session.cancel_reason = %v, want abandoned", got)
	}
}

func TestClineEventTaskErrorStaysAnError(t *testing.T) {
	logPath := clineTestLog(t)

	runHookWithInput(t, runClineEvent, map[string]interface{}{
		"hookName": "stop_error",
		"taskId":   "task-failed",
		"error":    map[string]interface{}{"name": "provider_timeout"},
	})

	event := clineEventWithAction(t, logPath, "session.error")
	if got := event["severity"]; got != "high" {
		t.Errorf("severity = %q, want high", got)
	}
	if got := nested(t, event, "error")["type"]; got != "provider_timeout" {
		t.Errorf("error.type = %q, want provider_timeout", got)
	}
}

// Cline's two hook surfaces disagree about where the cancel status lives. Missing the nested one
// labels an abandoned task as cancelled -- a wrong distinction rather than a missing one, which is
// why it is read from both.
func TestClineCancelReasonReadsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input map[string]interface{}
		want  string
	}{
		{
			name:  "top level status",
			input: map[string]interface{}{"completionStatus": "abandoned"},
			want:  "abandoned",
		},
		{
			name: "nested under taskCancel.taskMetadata",
			input: map[string]interface{}{
				"taskCancel": map[string]interface{}{
					"taskMetadata": map[string]interface{}{"completionStatus": "abandoned"},
				},
			},
			want: "abandoned",
		},
		{
			name:  "nested under taskMetadata",
			input: map[string]interface{}{"taskMetadata": map[string]interface{}{"completionStatus": "abandoned"}},
			want:  "abandoned",
		},
		{
			name:  "top level wins over nested",
			input: map[string]interface{}{"reason": "cancelled", "taskMetadata": map[string]interface{}{"completionStatus": "abandoned"}},
			want:  "cancelled",
		},
		{
			name:  "falls back to cancelled",
			input: map[string]interface{}{},
			want:  "cancelled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clineCancelReason(tc.input); got != tc.want {
				t.Errorf("clineCancelReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The plugin surface -- the one Beacon installs -- has a single run-completion handler for every
// outcome, so an aborted or failed task arrives as afterRun and says so in a field. Before this was
// read, both reached the log as a clean session.ended, which made the cancel and error paths
// unreachable through Beacon's own plugin no matter what the file-hook tests proved.
func TestClineEventAfterRunHonorsTheRunOutcome(t *testing.T) {
	for _, tc := range []struct {
		name         string
		payload      map[string]interface{}
		wantAction   string
		wantSeverity string
		wantCancel   string
		wantErrType  string
	}{
		{
			name:         "no status is still a success",
			payload:      map[string]interface{}{},
			wantAction:   "session.ended",
			wantSeverity: "info",
		},
		{
			name:         "explicit success",
			payload:      map[string]interface{}{"status": "completed"},
			wantAction:   "session.ended",
			wantSeverity: "info",
		},
		{
			name:         "aborted is a cancel",
			payload:      map[string]interface{}{"status": "aborted"},
			wantAction:   "session.ended",
			wantSeverity: "info",
			wantCancel:   "aborted",
		},
		{
			name:         "failed is an error",
			payload:      map[string]interface{}{"status": "failed"},
			wantAction:   "session.error",
			wantSeverity: "high",
			wantErrType:  "task_error",
		},
		{
			name:         "status on the nested result",
			payload:      map[string]interface{}{"result": map[string]interface{}{"status": "cancelled"}},
			wantAction:   "session.ended",
			wantSeverity: "info",
			wantCancel:   "cancelled",
		},
		{
			name:         "a failure names its error type when it has one",
			payload:      map[string]interface{}{"status": "failed", "error": map[string]interface{}{"name": "ProviderTimeout"}},
			wantAction:   "session.error",
			wantSeverity: "high",
			wantErrType:  "ProviderTimeout",
		},
		{
			// An unrecognized value must not be guessed at: a false session.error is worse than a
			// missing one in a log people alert on.
			name:         "an unknown status stays a success",
			payload:      map[string]interface{}{"status": "whatever-cline-adds-next"},
			wantAction:   "session.ended",
			wantSeverity: "info",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := clineTestLog(t)
			payload := map[string]interface{}{
				"type":   "afterRun",
				"taskId": "task-outcome",
				"usage":  map[string]interface{}{"inputTokens": 70, "outputTokens": 9},
			}
			for k, v := range tc.payload {
				payload[k] = v
			}
			runHookWithInput(t, runClineEvent, payload)

			event := clineEventWithAction(t, logPath, tc.wantAction)
			if got := event["severity"]; got != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", got, tc.wantSeverity)
			}
			if tc.wantCancel == "" {
				if session, ok := event["session"].(map[string]interface{}); ok {
					if reason, ok := session["cancel_reason"]; ok {
						t.Errorf("cancel_reason = %v, want none", reason)
					}
				}
			} else if got := nested(t, event, "session")["cancel_reason"]; got != tc.wantCancel {
				t.Errorf("session.cancel_reason = %v, want %q", got, tc.wantCancel)
			}
			if tc.wantErrType == "" {
				if _, ok := event["error"]; ok {
					t.Errorf("carried an error field: %v", event["error"])
				}
			} else if got := nested(t, event, "error")["type"]; got != tc.wantErrType {
				t.Errorf("error.type = %v, want %q", got, tc.wantErrType)
			}
			// Whatever the outcome, the tokens were spent and must be reported.
			if got := nested(t, event, "gen_ai", "usage")["input_tokens"]; got != float64(70) {
				t.Errorf("gen_ai.usage.input_tokens = %v, want 70", got)
			}
		})
	}
}
