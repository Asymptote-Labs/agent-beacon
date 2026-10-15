package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
)

type ClaudeOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type ClaudeStatus struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

var claudeRuntime = hookRuntime{
	displayName: "Claude Code",
	configPath:  claudeSettingsPath,
	install:     installClaudeSettings,
	uninstall:   removeClaudeEndpointHooks,
	isInstalled: isClaudeInstalledAt,
}

func InstallClaude(opts ClaudeOptions) (ClaudeStatus, error) {
	status, err := installRuntimeHooks(claudeRuntime, RuntimeOptions(opts))
	if err != nil {
		return ClaudeStatus{}, err
	}
	return claudeStatusFromRuntime(status), nil
}

func UninstallClaude(opts ClaudeOptions) (ClaudeStatus, error) {
	status, err := uninstallRuntimeHooks(claudeRuntime, RuntimeOptions(opts))
	if err != nil {
		return ClaudeStatus{}, err
	}
	return claudeStatusFromRuntime(status), nil
}

func ClaudeHookStatus(opts ClaudeOptions) ClaudeStatus {
	return claudeStatusFromRuntime(runtimeHookStatus(claudeRuntime, RuntimeOptions(opts)))
}

func IsClaudeInstalled(opts ClaudeOptions) bool {
	return isRuntimeInstalled(claudeRuntime, RuntimeOptions(opts))
}

func claudeStatusFromRuntime(status runtimeStatus) ClaudeStatus {
	return ClaudeStatus{
		Installed:    status.Installed,
		BinaryPath:   status.BinaryPath,
		SettingsPath: status.ConfigPath,
		Message:      status.Message,
	}
}

// Extension points for a build of Beacon that compiles in hook subcommands of its own (see
// version.Edition). Both are nil in this build, which installs exactly the hooks listed in
// installClaudeSettings.
var (
	// claudeExtraHookGroups returns further hook groups to install for Claude Code, keyed by
	// event, given the command prefix Beacon's own hooks use. Their commands carry that prefix,
	// so repair, status and uninstall treat them as Beacon's hooks: a reinstall replaces them and
	// an uninstall removes them.
	claudeExtraHookGroups func(prefix string) map[string][]settingsHookGroup

	// claudeSettingsChanged runs after Beacon has written its hooks into a Claude Code settings
	// file (installed true) or taken them out (installed false). It runs as whoever ran the
	// install, which for a system install's per-user repair is the console user, so files kept
	// next to the settings, such as a command under .claude/commands, stay in step with the hooks.
	claudeSettingsChanged func(settingsPath string, installed bool) error
)

func installClaudeSettings(path, binaryPath, logPath, configPath string) error {
	prefix := endpointCommandPrefix("claude", binaryPath, logPath, configPath)
	endpointHooks := map[string]settingsHookGroup{
		"SessionStart":       {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " session-start"}}},
		"UserPromptSubmit":   {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " prompt-submit", Timeout: 30}}},
		"PreToolUse":         {Matcher: "Bash|Edit|Write|MultiEdit|Read|Glob|Grep|WebFetch|WebSearch|Agent|mcp__.*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " pre-tool"}}},
		"PostToolUse":        {Matcher: "*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " post-tool"}}},
		"PostToolUseFailure": {Matcher: "*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " post-tool"}}},
		"Stop":               {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " stop", Timeout: 45}}},
		"SubagentStart":      {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " subagent-start"}}},
		"SubagentStop":       {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " subagent-stop"}}},
		"PermissionRequest":  {Matcher: "*", Hooks: []settingsHookRef{{Type: "command", Command: prefix + " permission-request"}}},
		"SessionEnd":         {Hooks: []settingsHookRef{{Type: "command", Command: prefix + " session-end"}}},
	}
	groups := make(map[string][]settingsHookGroup, len(endpointHooks))
	for event, group := range endpointHooks {
		groups[event] = []settingsHookGroup{group}
	}
	if claudeExtraHookGroups != nil {
		for event, extra := range claudeExtraHookGroups(prefix) {
			groups[event] = append(groups[event], extra...)
		}
	}
	if err := installSettingsEndpointHookGroups(path, "claude", groups); err != nil {
		return err
	}
	if claudeSettingsChanged != nil {
		return claudeSettingsChanged(path, true)
	}
	return nil
}

func removeClaudeEndpointHooks(path string) (bool, error) {
	removed, err := removeSettingsEndpointHooks(path, "claude")
	if err != nil {
		return removed, err
	}
	if claudeSettingsChanged != nil {
		if err := claudeSettingsChanged(path, false); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func isClaudeInstalledAt(path string) bool {
	return isSettingsEndpointInstalledAt(path, "claude")
}

func claudeSettingsPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir := claudesession.ConfigDir(home)
		if dir == "" {
			return "", fmt.Errorf("home directory is empty")
		}
		return filepath.Join(dir, "settings.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
