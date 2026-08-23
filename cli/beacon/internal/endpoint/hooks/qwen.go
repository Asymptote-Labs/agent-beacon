package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Qwen Code hook timeouts are milliseconds.
//
// This is the one place Qwen's settings format diverges from Claude Code's while looking identical,
// and it fails quietly in the direction that hurts. Claude Code reads `timeout` as seconds, so the
// values the Claude installer writes -- 30, 45, 10 -- mean 30ms, 45ms and 10ms to Qwen. A hook
// process cannot start, read stdin, resolve a git branch and append to a JSONL file in ten
// milliseconds, so every tool event would be killed mid-write. The install would report success,
// the settings file would look right, and the runtime log would be empty or truncated.
//
// Named constants rather than literals so the unit is stated at the value, and so
// TestQwenTimeoutsAreMillisecondsNotSeconds has something to assert against other than magic
// numbers. Qwen's own default is 60000 (60s); events with no entry here inherit it, which is the
// right answer for the ones that do no work beyond writing a line.
const (
	qwenPromptSubmitTimeoutMS = 30000
	qwenToolTimeoutMS         = 10000
	qwenStopTimeoutMS         = 45000
)

// qwenAllToolsMatcher matches every tool id.
//
// Qwen documents `""` and `"*"` as the two spellings that match all events of a type, and uses
// `"*"` in its own examples. `"*"` is preferred over an omitted matcher because Qwen's matcher
// table marks tool events as regex-filtered and its tool-event examples always carry one; an
// omitted matcher on a tool event is undocumented territory, and "documented and demonstrated"
// beats "probably equivalent" for the field that decides whether a hook fires at all.
const qwenAllToolsMatcher = "*"

// qwenHookShell tells Qwen which shell to parse Beacon's hook command with.
//
// Beacon builds that command with hookCommandQuote, which single-quotes every path on every
// platform. Single quotes are fully literal in a POSIX shell, which is what a Windows path needs --
// inside double quotes bash still expands `$` and eats a doubled backslash, so a profile directory
// containing a `$` would vanish and a UNC path would lose a separator. That function's own comment
// records the cost of the choice: the same quoting is not valid in cmd.exe, and in PowerShell a
// quoted string in command position is an expression rather than a command.
//
// Qwen is the runtime that comment anticipated. It runs hook commands through a shell of its
// choosing and documents exactly two values a hook may ask for, `bash` and `powershell`. Unlike
// Claude Code, which was measured to run hook commands through Git Bash on Windows regardless,
// Qwen only uses bash when the hook configuration asks for it. Left unset on Windows, a
// Node-hosted runtime lands on the platform default -- cmd.exe -- where
// Beacon's quotes are literal characters and every hook fails to start while the install still
// reports success. That is the same silent-failure shape as writing a seconds-valued timeout, and
// it is worth the same care.
//
// `bash` rather than `powershell` because the command is already POSIX-quoted: choosing the other
// would mean maintaining a second quoting dialect for one runtime. On macOS and Linux this names
// the shell that would have run anyway. On Windows it requires bash on PATH, which is the same
// requirement Claude Code already carries there -- and it strictly improves on the alternative,
// since cmd.exe cannot parse the command at all.
const qwenHookShell = "bash"

type QwenOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type QwenStatus struct {
	Installed    bool   `json:"installed"`
	BinaryPath   string `json:"binary_path,omitempty"`
	SettingsPath string `json:"settings_path,omitempty"`
	Message      string `json:"message,omitempty"`
}

var qwenRuntime = hookRuntime{
	displayName: "Qwen Code",
	configPath:  QwenSettingsPath,
	install:     installQwenSettings,
	uninstall:   removeQwenEndpointHooks,
	isInstalled: isQwenInstalledAt,
}

func InstallQwen(opts QwenOptions) (QwenStatus, error) {
	status, err := installRuntimeHooks(qwenRuntime, RuntimeOptions(opts))
	if err != nil {
		return QwenStatus{}, err
	}
	return qwenProjectTrustMessage(qwenStatusFromRuntime(status), opts.Level), nil
}

func UninstallQwen(opts QwenOptions) (QwenStatus, error) {
	status, err := uninstallRuntimeHooks(qwenRuntime, RuntimeOptions(opts))
	if err != nil {
		return QwenStatus{}, err
	}
	return qwenStatusFromRuntime(status), nil
}

func QwenHookStatus(opts QwenOptions) QwenStatus {
	return qwenProjectTrustMessage(qwenStatusFromRuntime(runtimeHookStatus(qwenRuntime, RuntimeOptions(opts))), opts.Level)
}

func IsQwenInstalled(opts QwenOptions) bool {
	return isRuntimeInstalled(qwenRuntime, RuntimeOptions(opts))
}

func qwenStatusFromRuntime(status runtimeStatus) QwenStatus {
	return QwenStatus{
		Installed:    status.Installed,
		BinaryPath:   status.BinaryPath,
		SettingsPath: status.ConfigPath,
		Message:      status.Message,
	}
}

// installQwenSettings merges Beacon's hooks into Qwen Code's settings.json.
//
// The file is shared with the user's own configuration -- model, theme, MCP servers, their own
// hooks -- so this goes through the same merge the Claude and Factory installers use: unrelated
// top-level keys are preserved verbatim, non-Beacon hooks in the same event group survive, and a
// re-install replaces Beacon's own entries instead of stacking a second copy beside them.
//
// The event set is everything Qwen exposes that carries endpoint signal. `PostToolUseFailure` is
// registered alongside `PostToolUse` because a failed tool is a separate event in Qwen and would
// otherwise be invisible; `PermissionRequest` is what makes an approval prompt observable at all.
// The fire-and-forget events Qwen also offers -- `MessageDisplay`, `StopFailure`, `PostCompact`,
// `SessionDelete`, the todo pair -- are deliberately not registered: `MessageDisplay` alone fires
// every ~200ms for the length of every reply, and spawning a process that often to record assistant
// text Beacon does not otherwise retain would cost more than it tells anyone.
func installQwenSettings(path, binaryPath, logPath, configPath string) error {
	prefix := endpointCommandPrefix("qwen", binaryPath, logPath, configPath)
	endpointHooks := map[string]settingsHookGroup{
		"SessionStart":       {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " session-start"}}},
		"UserPromptSubmit":   {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " prompt-submit", Timeout: qwenPromptSubmitTimeoutMS}}},
		"PreToolUse":         {Matcher: qwenAllToolsMatcher, Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " pre-tool", Timeout: qwenToolTimeoutMS}}},
		"PostToolUse":        {Matcher: qwenAllToolsMatcher, Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " post-tool", Timeout: qwenToolTimeoutMS}}},
		"PostToolUseFailure": {Matcher: qwenAllToolsMatcher, Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " post-tool", Timeout: qwenToolTimeoutMS}}},
		"PermissionRequest":  {Matcher: qwenAllToolsMatcher, Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " permission-request", Timeout: qwenToolTimeoutMS}}},
		"Stop":               {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " stop", Timeout: qwenStopTimeoutMS}}},
		"SubagentStart":      {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " subagent-start"}}},
		"SubagentStop":       {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " subagent-stop"}}},
		"SessionEnd":         {Hooks: []settingsHookRef{{Type: "command", Shell: qwenHookShell, Command: prefix + " session-end"}}},
	}
	return installSettingsEndpointHooks(path, "qwen", endpointHooks)
}

func readQwenSettings(path string) (settingsHooksFile, error) {
	return readSettingsHooks(path)
}

func removeQwenEndpointHooks(path string) (bool, error) {
	return removeSettingsEndpointHooks(path, "qwen")
}

func isQwenInstalledAt(path string) bool {
	return isSettingsEndpointInstalledAt(path, "qwen")
}

// QwenSettingsPath returns the settings file Beacon merges into for a given install level.
//
// Qwen Code resolves settings from `~/.qwen/settings.json` for the user and `.qwen/settings.json`
// in the project root, with project settings taking precedence. The user level is the default here
// for the same reason it is elsewhere: a project-level install only takes effect once the folder is
// trusted, so it is the one that needs a caveat rather than the one to reach for first.
//
// Exported because `harness` reports on the same file and must not rebuild the path itself. Two
// copies of "where the settings live" is how discovery comes to report on a file the installer does
// not write -- and here it would do so in the worst way: an unresolved home directory turns the
// user path into the *project* path, so discovery would read whatever repository the command ran
// from and report it as the machine's state.
func QwenSettingsPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".qwen", "settings.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".qwen", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

// qwenProjectTrustMessage says out loud that a project-level install is not yet collecting.
//
// Qwen's security model gates project-level hooks behind trusted folder status, so an install that
// reports plain success into an untrusted folder is reporting something untrue: the file is
// written, and nothing runs. Grok carries the same caveat for the same reason.
func qwenProjectTrustMessage(status QwenStatus, level Level) QwenStatus {
	if level == LevelProject && status.Message != "" && !strings.Contains(status.Message, "trusted") {
		status.Message += "; Qwen Code runs project hooks only in a trusted folder"
	}
	return status
}
