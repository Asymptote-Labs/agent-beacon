package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
)

type CodexOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type CodexStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

var codexRuntime = hookRuntime{
	displayName: "Codex CLI",
	configPath:  codexHooksPath,
	install:     installCodexHooks,
	uninstall:   removeCodexEndpointHooks,
	isInstalled: isCodexInstalledAt,
}

func InstallCodex(opts CodexOptions) (CodexStatus, error) {
	status, err := installRuntimeHooks(codexRuntime, RuntimeOptions(opts))
	if err != nil {
		return CodexStatus{}, err
	}
	return codexStatusFromRuntime(status), nil
}

func UninstallCodex(opts CodexOptions) (CodexStatus, error) {
	status, err := uninstallRuntimeHooks(codexRuntime, RuntimeOptions(opts))
	if err != nil {
		return CodexStatus{}, err
	}
	return codexStatusFromRuntime(status), nil
}

func CodexHookStatus(opts CodexOptions) CodexStatus {
	return codexStatusFromRuntime(runtimeHookStatus(codexRuntime, RuntimeOptions(opts)))
}

func IsCodexInstalled(opts CodexOptions) bool {
	return isRuntimeInstalled(codexRuntime, RuntimeOptions(opts))
}

func codexStatusFromRuntime(status runtimeStatus) CodexStatus {
	return CodexStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

// Extension points for a build of Beacon that compiles in hook subcommands of its own (see
// version.Edition). Both are nil in this build, which installs exactly the hooks listed in
// installCodexHooks.
var (
	// codexExtraHookGroups returns further hook groups to install for Codex, keyed by event,
	// given the command prefix Beacon's own hooks use; a group can carry a matcher. Their
	// commands carry that prefix, so repair, status and uninstall treat them as Beacon's hooks:
	// a reinstall removes every group Beacon wrote before writing the current ones, and an
	// uninstall removes them all.
	codexExtraHookGroups func(prefix string) map[string][]settingsHookGroup

	// codexHooksChanged runs after Beacon has written its hooks into a Codex hooks.json
	// (installed true) or taken them out (installed false), as whoever ran the install.
	codexHooksChanged func(hooksPath string, installed bool) error
)

func installCodexHooks(path, binaryPath, logPath, configPath string) error {
	prefix := endpointCommandPrefix("codex", binaryPath, logPath, configPath)
	// Hooks supply only what Codex's OTLP export lacks: the OS-user session context, and the turn's
	// final reply, which Codex's Stop payload carries as last_assistant_message and no span or log
	// does. Both are dedicated commands rather than the generic ones, because OTLP already reports
	// the session, prompts, tools and turn. Inventory used to ride SessionStart and
	// UserPromptSubmit here; it is written by the scheduled inventory job now.
	endpointHooks := map[string]settingsHookGroup{
		"SessionStart": {Hooks: []settingsHookRef{
			{Type: "command", Command: prefix + " codex-session-context", Timeout: 10},
		}},
		"Stop": {Hooks: []settingsHookRef{
			{Type: "command", Command: prefix + " codex-assistant-response", Timeout: 10},
		}},
	}
	groups := make(map[string][]settingsHookGroup, len(endpointHooks))
	for event, group := range endpointHooks {
		groups[event] = []settingsHookGroup{group}
	}
	if codexExtraHookGroups != nil {
		for event, extra := range codexExtraHookGroups(prefix) {
			groups[event] = append(groups[event], extra...)
		}
	}
	if err := installSettingsEndpointHookGroups(path, "codex", groups); err != nil {
		return err
	}
	if codexHooksChanged != nil {
		return codexHooksChanged(path, true)
	}
	return nil
}

func removeCodexEndpointHooks(path string) (bool, error) {
	removed, err := removeSettingsEndpointHooks(path, "codex")
	if err != nil {
		return removed, err
	}
	if codexHooksChanged != nil {
		if err := codexHooksChanged(path, false); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func isCodexInstalledAt(path string) bool {
	return isSettingsEndpointInstalledAt(path, "codex")
}

func codexHooksPath(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir, err := codexsession.HomeDir(home)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "hooks.json"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "hooks.json"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
