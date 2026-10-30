package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Level string

const (
	LevelUser    Level = "user"
	LevelProject Level = "project"
)

type HookRef struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type HooksJSON struct {
	Version int                  `json:"version"`
	Hooks   map[string][]HookRef `json:"hooks"`
}

type CursorOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type CursorCloudOptions struct {
	BinaryPath string
	LogPath    string
	SafeHooks  bool
}

type CursorStatus struct {
	Installed     bool   `json:"installed"`
	BinaryPath    string `json:"binary_path,omitempty"`
	HooksJSONPath string `json:"hooks_json_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

var cursorRuntime = hookRuntime{
	displayName: "Cursor",
	configPath:  cursorHooksJSONPath,
	install:     installCursorHooksJSON,
	uninstall:   removeCursorEndpointHooks,
	isInstalled: isCursorInstalledAt,
}

// Extension points for a build of Beacon that compiles in hook subcommands of its own (see
// version.Edition). Both are nil in this build, which installs exactly the hooks listed in
// installCursorHooksJSON.
var (
	// cursorExtraHooks returns further hook entries to install for Cursor, keyed by event, given
	// the command prefix Beacon's own hooks use. Their commands carry that prefix, so repair,
	// status and uninstall treat them as Beacon's hooks. Every event the map names is stripped of
	// Beacon's entries like the events Beacon installs, then gets Beacon's own entry, if it has
	// one, followed by these. An event named with no entries is stripped and left with only the
	// user's, so a build stops installing a hook by naming its event with none: an event left out
	// of the map keeps whatever Beacon wrote there until an uninstall.
	cursorExtraHooks func(prefix string) map[string][]HookRef

	// cursorHooksChanged runs after Beacon has written its hooks into a Cursor hooks.json
	// (installed true) or taken them out (installed false), as whoever ran the install.
	cursorHooksChanged func(hooksPath string, installed bool) error
)

func InstallCursor(opts CursorOptions) (CursorStatus, error) {
	status, err := installRuntimeHooks(cursorRuntime, RuntimeOptions(opts))
	if err != nil {
		return CursorStatus{}, err
	}
	return cursorStatusFromRuntime(status), nil
}

func UninstallCursor(opts CursorOptions) (CursorStatus, error) {
	status, err := uninstallRuntimeHooks(cursorRuntime, RuntimeOptions(opts))
	if err != nil {
		return CursorStatus{}, err
	}
	return cursorStatusFromRuntime(status), nil
}

func CursorHookStatus(opts CursorOptions) CursorStatus {
	return cursorStatusFromRuntime(runtimeHookStatus(cursorRuntime, RuntimeOptions(opts)))
}

func IsCursorInstalled(opts CursorOptions) bool {
	return isRuntimeInstalled(cursorRuntime, RuntimeOptions(opts))
}

func cursorStatusFromRuntime(status runtimeStatus) CursorStatus {
	return CursorStatus{
		Installed:     status.Installed,
		BinaryPath:    status.BinaryPath,
		HooksJSONPath: status.ConfigPath,
		Message:       status.Message,
	}
}

func installCursorHooksJSON(path, binaryPath, logPath, configPath string) error {
	hooksFile, err := readCursorHooksFile(path)
	if err != nil {
		return err
	}
	commandPrefix := endpointCommandPrefix("cursor", binaryPath, logPath, configPath)
	// Keep this event-to-subcommand wiring in step with cursorEventsForCommand in
	// cli/beacon-hooks/cmd/cursor_compat.go: it decides, from the subcommand alone, which Cursor
	// event a Claude Code hook running the same subcommand may trust Beacon's own Cursor entry to
	// also be covering, and a mismatch either suppresses an event this map no longer records or
	// keeps recording one it does.
	endpointHooks := map[string]HookRef{
		"sessionStart":       {Command: commandPrefix + " session-start"},
		"beforeSubmitPrompt": {Command: commandPrefix + " prompt-submit", Timeout: 30},
		"preToolUse":         {Command: commandPrefix + " pre-tool", Matcher: "Write|Edit|MultiEdit|Shell|MCP"},
		"afterAgentThought":  {Command: commandPrefix + " agent-thought"},
		"afterAgentResponse": {Command: commandPrefix + " agent-response"},
		"afterFileEdit":      {Command: commandPrefix + " post-tool"},
		"postToolUse":        {Command: commandPrefix + " post-tool"},
		"postToolUseFailure": {Command: commandPrefix + " post-tool"},
		"stop":               {Command: commandPrefix + " stop"},
		"sessionEnd":         {Command: commandPrefix + " session-end"},
	}
	entries := make(map[string][]json.RawMessage, len(endpointHooks))
	for hookName, ref := range endpointHooks {
		entry, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		entries[hookName] = []json.RawMessage{entry}
	}
	if cursorExtraHooks != nil {
		for hookName, refs := range cursorExtraHooks(commandPrefix) {
			list := entries[hookName]
			for _, ref := range refs {
				entry, err := json.Marshal(ref)
				if err != nil {
					return err
				}
				list = append(list, entry)
			}
			entries[hookName] = list
		}
	}
	for hookName, list := range entries {
		existing := hooksFile.hooks[hookName]
		merged := mergeEndpointHooks(existing, list)
		if len(merged) == 0 {
			// Only an extension's event can end up empty. Drop it when Beacon's entries were
			// all it held; an event that was already empty is the user's and stays as it was.
			if len(existing) > 0 {
				delete(hooksFile.hooks, hookName)
			}
			continue
		}
		hooksFile.hooks[hookName] = merged
	}
	data, err := hooksFile.marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	if cursorHooksChanged != nil {
		return cursorHooksChanged(path, true)
	}
	return nil
}

func RenderCursorCloudHooks(opts CursorCloudOptions) (string, error) {
	hooksJSON := HooksJSON{
		Version: 1,
		Hooks:   CursorCloudHookRefs(opts),
	}
	data, err := json.MarshalIndent(hooksJSON, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func CursorCloudHookRefs(opts CursorCloudOptions) map[string][]HookRef {
	if opts.LogPath == "" {
		opts.LogPath = "/tmp/beacon/runtime.jsonl"
	}
	prefix := cursorCloudCommandPrefix(opts.BinaryPath, opts.LogPath)
	refs := map[string][]HookRef{
		"beforeSubmitPrompt": {{Command: prefix + " prompt-submit", Timeout: 30}},
		"postToolUse":        {{Command: prefix + " post-tool"}},
		"postToolUseFailure": {{Command: prefix + " post-tool"}},
		"beforeReadFile": {
			{Command: prefix + " pre-tool"},
		},
		"subagentStart": {
			{Command: prefix + " subagent-start"},
		},
		"subagentStop": {
			{Command: prefix + " subagent-stop"},
		},
		"preCompact": {
			{Command: prefix + " cursor-event"},
		},
	}
	if !opts.SafeHooks {
		refs["beforeShellExecution"] = []HookRef{{Command: prefix + " pre-tool"}}
		refs["afterShellExecution"] = []HookRef{{Command: prefix + " post-tool"}}
		refs["afterFileEdit"] = []HookRef{{Command: prefix + " post-tool"}}
	}
	return refs
}

func cursorCloudCommandPrefix(binaryPath, logPath string) string {
	return fmt.Sprintf(
		"BEACON_ENDPOINT_MODE=1 BEACON_ORIGIN=cloud BEACON_RUN_PROVIDER=cursor_cloud BEACON_RUN_EPHEMERAL=true BEACON_ENDPOINT_LOG=%s %s --platform cursor",
		shellQuote(logPath),
		shellQuote(binaryPath),
	)
}

// cursorHooksFile is a Cursor hooks.json as read from disk. Beacon owns only the command entries it
// wrote, so everything else is held as the JSON it was read as and written back unchanged: other
// top-level keys, and every other hook entry with all of its fields. Cursor's hook options include
// `type` ("command" or "prompt"), `prompt`, `model`, `loop_limit` and `failClosed` besides the
// three HookRef models. Decoding the file into HookRef and encoding it back deleted them, which
// turned a user's fail-closed guard into a fail-open one and a prompt hook into `{"command":""}`.
type cursorHooksFile struct {
	values map[string]json.RawMessage
	hooks  map[string][]json.RawMessage
}

func readCursorHooksFile(path string) (cursorHooksFile, error) {
	file := cursorHooksFile{
		values: map[string]json.RawMessage{},
		hooks:  map[string][]json.RawMessage{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &file.values); err != nil {
			return cursorHooksFile{}, err
		}
		if file.values == nil {
			file.values = map[string]json.RawMessage{}
		}
		if rawHooks, ok := file.values["hooks"]; ok {
			if err := json.Unmarshal(rawHooks, &file.hooks); err != nil {
				return cursorHooksFile{}, err
			}
		}
	} else if !os.IsNotExist(err) {
		return cursorHooksFile{}, err
	}
	if file.hooks == nil {
		file.hooks = map[string][]json.RawMessage{}
	}
	return file, nil
}

// marshal encodes the file with Beacon's indentation. A missing or zero version is written as 1,
// as Cursor requires a positive version; any other version is kept as it was.
func (file cursorHooksFile) marshal() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(file.values)+2)
	for key, value := range file.values {
		out[key] = value
	}
	var version int
	if raw, ok := out["version"]; !ok || (json.Unmarshal(raw, &version) == nil && version == 0) {
		out["version"] = json.RawMessage("1")
	}
	hooks, err := json.Marshal(file.hooks)
	if err != nil {
		return nil, err
	}
	out["hooks"] = hooks
	return json.MarshalIndent(out, "", "  ")
}

// onlyHooks reports whether the file holds nothing besides its version and hooks, so removing
// Beacon's last hook may remove the file instead of leaving an empty one.
func (file cursorHooksFile) onlyHooks() bool {
	for key := range file.values {
		if key != "version" && key != "hooks" {
			return false
		}
	}
	return len(file.hooks) == 0
}

// cursorHookCommand returns the command of one hook entry, or "" for an entry with no string
// command (a prompt hook, or anything Beacon cannot read), which is never Beacon's.
func cursorHookCommand(entry json.RawMessage) string {
	var ref struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(entry, &ref); err != nil {
		return ""
	}
	return ref.Command
}

func isEndpointHookEntry(entry json.RawMessage) bool {
	return isEndpointHook(cursorHookCommand(entry))
}

// mergeEndpointHooks keeps an event's entries that are not Beacon's, in order, and appends refs.
func mergeEndpointHooks(existing []json.RawMessage, refs []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(existing))
	for _, item := range existing {
		if !isEndpointHookEntry(item) {
			out = append(out, item)
		}
	}
	return append(out, refs...)
}

func removeCursorEndpointHooks(path string) (bool, error) {
	removed, err := removeEndpointHooks(path)
	if err != nil {
		return removed, err
	}
	if cursorHooksChanged != nil {
		if err := cursorHooksChanged(path, false); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func removeEndpointHooks(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	file, err := readCursorHooksFile(path)
	if err != nil {
		return false, err
	}
	changed := false
	for hookName, entries := range file.hooks {
		filtered := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			if isEndpointHookEntry(entry) {
				continue
			}
			filtered = append(filtered, entry)
		}
		if len(filtered) == len(entries) {
			continue
		}
		changed = true
		if len(filtered) == 0 {
			delete(file.hooks, hookName)
		} else {
			file.hooks[hookName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	if file.onlyHooks() {
		return true, os.Remove(path)
	}
	out, err := file.marshal()
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, 0644)
}

func isEndpointHook(command string) bool {
	return isEndpointHookCommand(command, "cursor")
}

func isCursorInstalledAt(path string) bool {
	file, err := readCursorHooksFile(path)
	if err != nil {
		return false
	}
	for _, entries := range file.hooks {
		for _, entry := range entries {
			if isEndpointHookEntry(entry) {
				return true
			}
		}
	}
	return false
}

func cursorHooksJSONPath(level Level) (string, error) {
	targetDir, err := cursorTargetDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(targetDir, "hooks.json"), nil
}

func cursorTargetDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cursor"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".cursor"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
