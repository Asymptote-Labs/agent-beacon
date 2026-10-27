package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// cursorRecordsThisHook reports whether Beacon's own Cursor hooks run command, so that a run of
// Beacon's Claude Code hook carrying a Cursor payload is a second recording of the same event.
//
// The Cursor IDE runs the hooks in the user-level ~/.claude/settings.json alongside its own
// hooks.json, giving them Cursor payloads. With Beacon installed for both, every session start,
// prompt and stop was written twice, and every tool call that both pre-tool matchers name. Cursor's
// own entry decides what Beacon records in Cursor, so the Claude copy steps aside, command by
// command: where Beacon's Cursor hooks run no such command (subagent events, permission requests),
// or are not installed at the user level at all, the Claude copy is still the only one and records
// as before.
//
// It is one read of a small file, made only when a Claude Code hook receives a Cursor payload.
func cursorRecordsThisHook(command string) bool {
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
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &file) != nil {
		return false
	}
	for _, entries := range file.Hooks {
		for _, entry := range entries {
			if isBeaconCursorHookCommand(entry.Command, command) {
				return true
			}
		}
	}
	return false
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
