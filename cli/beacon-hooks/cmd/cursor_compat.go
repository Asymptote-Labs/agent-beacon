package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
// Claude copy steps aside only where a Beacon Cursor entry runs the same command and its matcher
// selects the payload's tool. The two installers' pre-tool matchers differ (Claude's names Read,
// Grep and Glob; Cursor's names Shell and MCP), so a call only Claude's matcher selects still has
// the Claude copy as its one recorder and policy check. So does any command Beacon's Cursor hooks
// don't run (subagent events, permission requests), and everything when they aren't installed at
// the user level.
//
// It is one read of a small file, made only when a Claude Code hook receives a Cursor payload.
func cursorRecordsThisHook(command string, input map[string]interface{}) bool {
	if command == "" {
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
	for _, entries := range file.Hooks {
		for _, entry := range entries {
			if isBeaconCursorHookCommand(entry.Command, command) && cursorMatcherSelects(entry.Matcher, tool) {
				return true
			}
		}
	}
	return false
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

// isBeaconCursorHookCommand reports whether line is a beacon-hooks command for Cursor that runs
// command. Beacon writes `'<dir>/beacon-hooks' --platform cursor [--log ... --config ...] <command>`,
// and older versions put endpoint settings in front as `VAR=value` words; both end with the command.
func isBeaconCursorHookCommand(line, command string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.Trim(fields[len(fields)-1], `'"`) != command {
		return false
	}
	binary, cursor := false, false
	for i, field := range fields {
		word := strings.Trim(field, `'"`)
		name := strings.TrimSuffix(filepath.Base(strings.ReplaceAll(word, `\`, "/")), ".exe")
		if name == "beacon-hooks" {
			binary = true
		}
		if word == "--platform=cursor" || (word == "--platform" && i+1 < len(fields) && strings.Trim(fields[i+1], `'"`) == "cursor") {
			cursor = true
		}
	}
	return binary && cursor
}
