package cmd

import (
	"path/filepath"
	"regexp"
	"testing"
)

// leaf reads a string at a dotted path, returning "" for anything missing along
// the way. nested is the fatal-on-missing variant and is the wrong tool here:
// half these assertions are about a field legitimately not being there.
func leaf(event map[string]interface{}, keys ...string) string {
	var current interface{} = event
	for _, key := range keys {
		next, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = next[key]
	}
	value, _ := current.(string)
	return value
}

func callIDOfEvent(event map[string]interface{}) string {
	return leaf(event, "gen_ai", "tool", "call", "id")
}

// Claude Code puts tool_use_id at the top level of the hook payload, outside
// tool_input, so no amount of reading the tool arguments would ever have found
// it. It is the only value that links this event to the tool's result, to the
// approval that allowed it, and to the OTLP record of the same command.
func TestRunPostToolPromotesClaudeToolUseID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "claude-session",
		"cwd":             "/repo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "toolu_01abc",
		"tool_input":      map[string]interface{}{"command": "echo hi"},
		"tool_response":   map[string]interface{}{"stdout": "hi"},
	})

	events := endpointEvents(t, logPath)
	if len(events) != 1 {
		t.Fatalf("event count = %d, want one command event: %#v", len(events), events)
	}
	if got := leaf(events[0], "event", "action"); got != "command.executed" {
		t.Fatalf("event.action = %q, want command.executed", got)
	}
	if got := callIDOfEvent(events[0]); got != "toolu_01abc" {
		t.Fatalf("gen_ai.tool.call.id = %q, want the tool_use_id Claude Code assigned", got)
	}
}

// A Claude Edit reaches the log through recordLocalEdit, which writes its event
// without going through emitHookEvent. Every file edit is recorded twice --
// once here, once from OTLP -- so this is the path that most needs the join key.
func TestRunPostToolPromotesToolUseIDOnFileEdits(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "claude-session",
		"cwd":             dir,
		"hook_event_name": "PostToolUse",
		"tool_name":       "Write",
		"tool_use_id":     "toolu_01write",
		"tool_input": map[string]interface{}{
			"file_path": filepath.Join(dir, "notes.md"),
			"content":   "hello",
		},
	})

	events := endpointEvents(t, logPath)
	edits := 0
	for _, event := range events {
		if leaf(event, "event", "action") != "file.modified" {
			continue
		}
		edits++
		if got := callIDOfEvent(event); got != "toolu_01write" {
			t.Fatalf("file.modified call ID = %q, want the tool_use_id Claude Code assigned", got)
		}
	}
	if edits == 0 {
		t.Fatalf("no file.modified event was written: %#v", events)
	}
}

// A session lifecycle payload that happens to echo an ID is not a tool call.
// Giving it the join key of one would join it to work it did not do.
func TestSessionEventsDoNotTakeAToolCallID(t *testing.T) {
	fields := map[string]interface{}{"session": map[string]interface{}{"id": "s1"}}
	applyToolCallID(fields, map[string]interface{}{"tool_use_id": "toolu_01abc"})
	if _, ok := fields["gen_ai"]; ok {
		t.Fatalf("a session event took a tool call ID: %#v", fields)
	}
}

// An MCP event already builds a gen_ai object for its arguments, so the call ID
// has to be written into it rather than over it.
func TestToolCallIDDoesNotDisplaceMCPToolFields(t *testing.T) {
	fields := toolFieldsWithResponse("mcp__linear__list_issues", map[string]interface{}{
		"mcp_server": "linear",
		"call_id":    "call_42",
		"team":       "beacon",
	}, nil)
	if got := callIDOfEvent(fields); got != "call_42" {
		t.Fatalf("gen_ai.tool.call.id = %q, want call_42", got)
	}
	if got := leaf(fields, "gen_ai", "tool", "name"); got != "list_issues" {
		t.Fatalf("gen_ai.tool.name = %q, want the MCP tool name to survive", got)
	}
	if got := nested(t, fields, "gen_ai", "tool", "call")["arguments"]; got == nil {
		t.Fatal("gen_ai.tool.call.arguments was dropped when the call ID was written")
	}
}

var uuidV5Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Every event the hook writer emits carries an identity, whether or not the
// runtime named the action itself.
func TestHookEventsCarryAnEventID(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "claude"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	runHookWithInput(t, runPostTool, map[string]interface{}{
		"session_id":      "claude-session",
		"cwd":             "/repo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "toolu_01abc",
		"tool_input":      map[string]interface{}{"command": "echo hi"},
	})

	for _, event := range endpointEvents(t, logPath) {
		id := leaf(event, "event", "id")
		if !uuidV5Pattern.MatchString(id) {
			t.Fatalf("event.id = %q, want an RFC 4122 version 5 UUID: %#v", id, event)
		}
	}
}
