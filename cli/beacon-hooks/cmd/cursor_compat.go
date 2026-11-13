package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/hookcmd"
)

// cursorUserHooksPath locates the user-level Cursor hooks file, where `beacon endpoint install`
// writes Beacon's Cursor hooks. Tests replace it.
var cursorUserHooksPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "hooks.json")
}

// cursorRecordsThisHook reports whether Beacon's own Cursor hooks run command for this payload, so
// that a run of Beacon's Claude Code hook carrying a Cursor payload is a second recording of the
// same event.
//
// The Cursor IDE runs the hooks in the user-level ~/.claude/settings.json alongside its own
// hooks.json, giving them Cursor payloads. With Beacon installed for both, every session start,
// prompt and stop was written twice, and every tool call that both pre-tool matchers name. The
// Claude copy steps aside only where, under an event Beacon's installer wires that subcommand to
// (hookcmd.CursorEvents), a Beacon Cursor entry's matcher selects the payload's tool. An entry
// elsewhere in the file -- a stale one from a hand edit -- cannot claim a subcommand it was never
// wired to run for. The two installers'
// pre-tool matchers differ (Claude's names Read, Grep and Glob; Cursor's names Shell and MCP), so
// a call only Claude's matcher selects still has the Claude copy as its one recorder and policy
// check. So does any command Beacon's Cursor hooks don't run (subagent events, permission
// requests), and everything when they aren't installed at the user level.
//
// It is one read of a small file, made only when a Claude Code hook receives a Cursor payload.
func cursorRecordsThisHook(command string, input map[string]interface{}) bool {
	events := hookcmd.CursorEvents[command]
	if len(events) == 0 {
		return false
	}
	path := cursorUserHooksPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var file struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &file) != nil {
		return false
	}
	tool := getFirstStr(input, "tool_name", "toolName")
	for _, event := range events {
		for _, entry := range file.Hooks[event] {
			if hookcmd.Runs(entry.Command, "cursor", command) && cursorMatcherSelects(entry.Matcher, tool) {
				return true
			}
		}
	}
	return false
}

// cursorRelaysCloudAgent reports whether this payload is the Cursor desktop app running a hook on
// this machine for a cloud agent's event.
//
// A cloud agent runs in Cursor's VM, which loads only project, team and enterprise hooks. While the
// desktop app's Agents Window shows one, it also runs the user-level hooks here, but only for
// afterAgentThought, subagentStart and subagentStop (measured on Cursor 3.29 and 3.30): no session
// start, prompt, tool call, response or stop. Recorded, those made a session of reasoning and
// subagent summaries attributed to this machine, with nothing to say the agent never ran on it.
//
// Such a payload names the cloud agent ("bc-<uuid>") or one of its subagents ("task-call-<id>")
// and no workspace. A cloud subagent's reasoning carries no parent id to tie it to its agent, so
// the prefix alone has to say it: local agents and their subagents get plain UUIDs (every local
// subagent transcript Cursor 3.30 had written was named by one). A cloud agent on a self-hosted
// worker has the same kind of id but runs in a
// checkout, so a hook there keeps recording, as does every hook in a cloud sandbox
// (BEACON_ORIGIN=cloud), where the project hooks from `beacon cloud cursor print-hooks` record
// the agent itself.
func cursorRelaysCloudAgent(input map[string]interface{}) bool {
	if strings.TrimSpace(os.Getenv("BEACON_ORIGIN")) == "cloud" {
		return false
	}
	id := getFirstStr(input, "conversation_id", "parent_conversation_id")
	if !strings.HasPrefix(id, "bc-") && !strings.HasPrefix(id, "task-call-") {
		return false
	}
	roots, _ := input["workspace_roots"].([]interface{})
	for _, root := range roots {
		if s, ok := root.(string); ok && strings.TrimSpace(s) != "" {
			return false
		}
	}
	return true
}

// cursorMatcherSelects reports whether a Cursor hook entry's matcher selects tool. An entry with no
// matcher runs for every call. Cursor reads a matcher as a pattern found anywhere in the tool's
// name, which is how Beacon's "MCP" selects "MCP:slack_send_message". A matcher that isn't a valid
// pattern, or a call that names no tool, counts as not selected, which leaves the Claude Code run
// recording: a duplicate beats a lost event.
func cursorMatcherSelects(matcher, tool string) bool {
	if strings.TrimSpace(matcher) == "" {
		return true
	}
	if tool == "" {
		return false
	}
	pattern, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	return pattern.MatchString(tool)
}
