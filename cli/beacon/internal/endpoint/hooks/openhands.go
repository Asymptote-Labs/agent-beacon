package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OpenHands registers hooks in one file, `.openhands/hooks.json`, which the runtime reads directly
// and which is also where a user keeps their own hooks. Beacon is a guest in it, so it merges its
// commands in and leaves everything else byte for byte -- unlike Muse Code, where Beacon owns a
// file outright because the runtime gave it one to own.
//
// The file is stricter than it looks, and every rule below is a rule the runtime enforces by
// silently dropping hooks rather than by reporting an error:
//
//   - Its top level is the event map itself. HookConfig forbids extra fields, so one unrecognized
//     top-level key fails validation and the whole file loads as nothing -- which rules out the
//     marker key Beacon stamps on the files it owns elsewhere. Detection is by the hook command
//     instead, which is the same thing every settings-file runtime already does.
//
//   - A legacy wrapper form, `{"hooks": {...}}`, is also accepted, and the loader unwraps it by
//     replacing the whole document with what is under that key. So adding a top-level event
//     alongside an existing `hooks` wrapper does not merge -- it discards Beacon's hooks with a
//     log line nobody sees. Beacon writes inside the wrapper when it finds one.
//
//   - Event names may be snake_case or Claude Code's PascalCase, and providing both spellings of
//     one event is a hard error that takes the entire file down, not just that event. So Beacon
//     writes under whichever spelling the file already uses for an event and never introduces a
//     second one.
//
// A file that already breaks one of these is refused rather than written into, because installing
// into it would report success and collect nothing.

const openHandsHookFileName = "hooks.json"

// openHandsHookName marks Beacon's hook definitions for a human reading the file.
//
// `name` is a documented HookDefinition field rather than an extra key, so it costs nothing and
// survives validation. It is deliberately not what uninstall and status match on: a user can edit
// it, and the command is what actually identifies a Beacon hook.
const openHandsHookName = "beacon-endpoint-telemetry"

// openHandsWrapperKey is the legacy Claude Code wrapper the loader unwraps the document into.
const openHandsWrapperKey = "hooks"

// openHandsAllToolsMatcher matches every tool.
//
// Required in spirit rather than in syntax: the matcher defaults to "*" when omitted, and the four
// events that are not tool-scoped ignore it entirely. Written on every group so the file says what
// it does instead of relying on a default.
const openHandsAllToolsMatcher = "*"

// OpenHands hook timeouts are seconds, and the runtime's own default is 60.
//
// Stated at the value for the reason the Qwen and Muse constants give: the same `timeout` field
// means milliseconds on Qwen and seconds here, and the two files look alike. Explicit values
// rather than the inherited default because 60 seconds is a long time to hold an agent turn for a
// telemetry hook that finishes in milliseconds -- these are ceilings on a hang, not budgets.
//
// The two long ones are long for a reason: prompt submission may run the inventory heartbeat, and
// the closing events flush cloud telemetry, which has a ten-second timeout of its own.
const (
	openHandsSessionStartTimeoutSeconds = 10
	openHandsPromptSubmitTimeoutSeconds = 30
	openHandsToolTimeoutSeconds         = 10
	openHandsStopTimeoutSeconds         = 45
	openHandsSessionEndTimeoutSeconds   = 45
)

// openHandsEvent binds one OpenHands lifecycle event to the beacon-hooks subcommand that maps it.
//
// Both spellings of the event name are carried because both are accepted and only one of them may
// appear in a given file. The PascalCase alias is not written by Beacon on a fresh install -- the
// snake_case name is what the runtime's own writer emits and what its reference documents -- but
// it has to be recognized, because a hooks.json shared with Claude Code will use it.
type openHandsEvent struct {
	snakeKey   string
	pascalKey  string
	subcommand string
	timeout    int
}

// openHandsEvents are the six events OpenHands exposes, all six of which Beacon subscribes to.
//
// There is nothing left out here, unlike the Muse and Cline mappings: OpenHands sends no model-call
// event, no compaction event and no subagent event, so this is its whole surface. What that costs
// is recorded where it is felt rather than here -- no token usage, and no approval decisions.
var openHandsEvents = []openHandsEvent{
	{"session_start", "SessionStart", "session-start", openHandsSessionStartTimeoutSeconds},
	{"user_prompt_submit", "UserPromptSubmit", "prompt-submit", openHandsPromptSubmitTimeoutSeconds},
	{"pre_tool_use", "PreToolUse", "pre-tool", openHandsToolTimeoutSeconds},
	{"post_tool_use", "PostToolUse", "post-tool", openHandsToolTimeoutSeconds},
	{"stop", "Stop", "stop", openHandsStopTimeoutSeconds},
	{"session_end", "SessionEnd", "session-end", openHandsSessionEndTimeoutSeconds},
}

type OpenHandsOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type OpenHandsStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

// openHandsHookGroup is one matcher group: a tool pattern and the hooks it fires.
//
// Hooks are held as raw JSON rather than as a typed slice so that a user's own hook definition
// round-trips byte for byte. HookDefinition has nine fields across three hook types, two of which
// (prompt and agent hooks) Beacon never writes, and a typed slice would quietly rewrite a user's
// entry into whatever subset this struct happened to model.
type openHandsHookGroup struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []json.RawMessage `json:"hooks"`
}

// openHandsHookRef is a command hook definition, holding only the fields Beacon writes.
//
// `type` and `command` are the contract; `name` is the marker; `timeout` is the ceiling. The
// remaining accepted fields belong to hook types Beacon does not use -- prompt, system_prompt,
// tools, max_iterations -- and `async` is deliberately absent: an async hook is fire-and-forget
// with its stdout discarded, which would make the policy seam's deny unreachable on the one event
// that can block.
type openHandsHookRef struct {
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// openHandsHooksFile is a parsed hooks.json, remembering the shape it arrived in.
//
// wrapped and the per-event key spellings are both load-bearing on write: putting the events back
// under the wrong shape is how a merge silently discards either Beacon's hooks or the user's.
type openHandsHooksFile struct {
	wrapped bool
	events  map[string][]openHandsHookGroup
}

var openHandsRuntime = hookRuntime{
	displayName: "OpenHands",
	configPath:  openHandsHooksPath,
	install:     installOpenHandsHooks,
	uninstall:   removeOpenHandsHooks,
	isInstalled: isOpenHandsInstalledAt,
}

func InstallOpenHands(opts OpenHandsOptions) (OpenHandsStatus, error) {
	status, err := installRuntimeHooks(openHandsRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenHandsStatus{}, err
	}
	return openHandsStatusFromRuntime(status), nil
}

func UninstallOpenHands(opts OpenHandsOptions) (OpenHandsStatus, error) {
	status, err := uninstallRuntimeHooks(openHandsRuntime, RuntimeOptions(opts))
	if err != nil {
		return OpenHandsStatus{}, err
	}
	return openHandsStatusFromRuntime(status), nil
}

func OpenHandsHookStatus(opts OpenHandsOptions) OpenHandsStatus {
	return openHandsStatusFromRuntime(runtimeHookStatus(openHandsRuntime, RuntimeOptions(opts)))
}

func IsOpenHandsInstalled(opts OpenHandsOptions) bool {
	return isRuntimeInstalled(openHandsRuntime, RuntimeOptions(opts))
}

func openHandsStatusFromRuntime(status runtimeStatus) OpenHandsStatus {
	return OpenHandsStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

// installOpenHandsHooks merges Beacon's six hooks into hooks.json, replacing any it wrote before.
func installOpenHandsHooks(path, binaryPath, logPath, configPath string) error {
	file, err := readOpenHandsHooks(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix("openhands", binaryPath, logPath, configPath)
	for _, event := range openHandsEvents {
		key := file.keyForEvent(event)
		ref, err := json.Marshal(openHandsHookRef{
			Type:    "command",
			Name:    openHandsHookName,
			Command: prefix + " " + event.subcommand,
			Timeout: event.timeout,
		})
		if err != nil {
			return err
		}
		groups := removeOpenHandsGroupHooks(file.events[key])
		file.events[key] = append(groups, openHandsHookGroup{
			Matcher: openHandsAllToolsMatcher,
			Hooks:   []json.RawMessage{ref},
		})
	}
	return writeOpenHandsHooks(path, file)
}

// keyForEvent returns the spelling this file already uses for an event, or the canonical
// snake_case name when the event is absent.
//
// Introducing a second spelling of an event the file already names is what this exists to prevent:
// the loader raises on a duplicate event and the entire file -- every hook in it, Beacon's and the
// user's -- loads as nothing.
func (file openHandsHooksFile) keyForEvent(event openHandsEvent) string {
	if _, ok := file.events[event.pascalKey]; ok {
		return event.pascalKey
	}
	return event.snakeKey
}

// readOpenHandsHooks parses hooks.json, or returns an empty file when there is none.
//
// A file that OpenHands itself would reject is an error rather than something to merge into.
// Beacon cannot fix it, and writing into it would report a successful install while the runtime
// went on loading nothing at all -- which is the failure mode this whole file is arranged against.
func readOpenHandsHooks(path string) (openHandsHooksFile, error) {
	file := openHandsHooksFile{events: map[string][]openHandsHookGroup{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return openHandsHooksFile{}, err
	}
	// An empty file is not malformed JSON to a person, and treating it as an error would make
	// install fail on a hooks.json somebody had created but not written.
	if len(strings.TrimSpace(string(data))) == 0 {
		return file, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return openHandsHooksFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if wrapper, ok := document[openHandsWrapperKey]; ok {
		file.wrapped = true
		// The loader replaces the document with the wrapper's contents, so anything beside it is
		// already being dropped by OpenHands. Beacon reads only the wrapper for the same reason,
		// and leaves the siblings where they are rather than merging them in.
		document = map[string]json.RawMessage{}
		if err := json.Unmarshal(wrapper, &document); err != nil {
			return openHandsHooksFile{}, fmt.Errorf("read %s: %w", path, err)
		}
	}
	for key, raw := range document {
		var groups []openHandsHookGroup
		if err := json.Unmarshal(raw, &groups); err != nil {
			return openHandsHooksFile{}, fmt.Errorf("read %s: hook event %q: %w", path, key, err)
		}
		file.events[key] = groups
	}
	if err := validateOpenHandsEventKeys(path, file.events); err != nil {
		return openHandsHooksFile{}, err
	}
	return file, nil
}

// validateOpenHandsEventKeys refuses a hooks.json OpenHands would refuse.
//
// Two ways it can already be broken, both silent at runtime and both worse after an install than
// before it, because the install would look like it worked.
func validateOpenHandsEventKeys(path string, events map[string][]openHandsHookGroup) error {
	known := map[string]bool{}
	for _, event := range openHandsEvents {
		known[event.snakeKey] = true
		known[event.pascalKey] = true
		if _, snake := events[event.snakeKey]; snake {
			if _, pascal := events[event.pascalKey]; pascal && event.snakeKey != event.pascalKey {
				return fmt.Errorf(
					"%s names the same hook event twice, as %q and %q; OpenHands rejects the whole "+
						"file when it does, so remove one spelling and run the install again",
					path, event.snakeKey, event.pascalKey)
			}
		}
	}
	unknown := []string{}
	for key := range events {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf(
			"%s has hook events OpenHands does not accept (%s); it rejects the whole file rather "+
				"than the unknown entry, so Beacon's hooks would never run. Remove them and run "+
				"the install again",
			path, strings.Join(unknown, ", "))
	}
	return nil
}

// writeOpenHandsHooks serializes the file back in the shape it arrived in.
func writeOpenHandsHooks(path string, file openHandsHooksFile) error {
	var document any = file.events
	if file.wrapped {
		document = map[string]any{openHandsWrapperKey: file.events}
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// 0644 rather than the 0600 the settings-file installers use: this is a project file that gets
	// committed and read by everyone working in the repository, and a mode only its author can read
	// would make the hooks stop working for the next person to check it out.
	return os.WriteFile(path, data, 0644)
}

// removeOpenHandsGroupHooks strips Beacon's hook definitions out of an event's matcher groups,
// dropping a group that has nothing left in it and keeping every other group untouched.
func removeOpenHandsGroupHooks(groups []openHandsHookGroup) []openHandsHookGroup {
	filtered := make([]openHandsHookGroup, 0, len(groups))
	for _, group := range groups {
		kept := make([]json.RawMessage, 0, len(group.Hooks))
		for _, hook := range group.Hooks {
			if isOpenHandsEndpointHook(hook) {
				continue
			}
			kept = append(kept, hook)
		}
		if len(kept) == 0 {
			continue
		}
		group.Hooks = kept
		filtered = append(filtered, group)
	}
	return filtered
}

// isOpenHandsEndpointHook reports whether one hook definition is one Beacon wrote.
//
// The command, not the name. `name` is a field a user can edit and a field another tool could
// happen to reuse, while the command carries the hook binary's path and `--platform openhands` --
// which is the same evidence every other runtime's detection uses, through the same predicate.
func isOpenHandsEndpointHook(raw json.RawMessage) bool {
	var ref struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return false
	}
	return isEndpointHookCommand(ref.Command, "openhands")
}

// removeOpenHandsHooks takes Beacon's hooks back out and leaves the user's in place.
//
// The file is deleted only when Beacon removed something and nothing at all is left, so an
// uninstall does not leave an empty hooks.json behind in somebody's repository. A file that still
// holds another tool's hooks is rewritten without Beacon's rather than removed.
func removeOpenHandsHooks(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	file, err := readOpenHandsHooks(path)
	if err != nil {
		return false, err
	}
	// Counted rather than compared position by position: removeOpenHandsGroupHooks drops groups
	// that end up empty, so the filtered slice does not line up with the original by index and a
	// pairwise comparison would read one group against another.
	changed := false
	for key, groups := range file.events {
		filtered := removeOpenHandsGroupHooks(groups)
		if openHandsHookCount(filtered) != openHandsHookCount(groups) {
			changed = true
		}
		if len(filtered) == 0 {
			delete(file.events, key)
			continue
		}
		file.events[key] = filtered
	}
	if !changed {
		return false, nil
	}
	if len(file.events) == 0 {
		return true, os.Remove(path)
	}
	return true, writeOpenHandsHooks(path, file)
}

func openHandsHookCount(groups []openHandsHookGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Hooks)
	}
	return total
}

func isOpenHandsInstalledAt(path string) bool {
	file, err := readOpenHandsHooks(path)
	if err != nil {
		return false
	}
	for _, groups := range file.events {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if isOpenHandsEndpointHook(hook) {
					return true
				}
			}
		}
	}
	return false
}

func openHandsHooksPath(level Level) (string, error) {
	dir, err := openHandsConfigDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, openHandsHookFileName), nil
}

// openHandsConfigDir resolves the directory holding hooks.json for a scope.
//
// Both scopes are real, and they are not equivalent -- which is the opposite of the Muse and
// Hermes situation, where the project scope does nothing at all.
//
// Project scope, `<cwd>/.openhands`, is the documented location and the one that works everywhere:
// Cloud, the CLI and the local GUI all read it, and the agent server reads only it.
//
// User scope, `~/.openhands`, is the SDK loader's second search location. It is a real path but a
// narrower one, and two things about it are worth knowing before choosing it: the loader takes the
// first location that exists rather than merging, so a repository with its own .openhands/hooks.json
// shadows it entirely; and the agent server, which is what the CLI and the GUI talk to, does not
// consult it at all. Beacon writes where it is told and says this in the docs rather than picking
// for the operator, because a user-scope install is exactly right for someone running the SDK
// directly and exactly wrong for someone running the packaged CLI.
//
// OH_PERSISTENCE_DIR takes precedence at user scope, matching how the SDK resolves it. Sandboxed
// and containerized setups redirect state onto a volume with it, and on a machine that sets it
// ~/.openhands is not where OpenHands looks.
func openHandsConfigDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if base := strings.TrimSpace(os.Getenv("OH_PERSISTENCE_DIR")); base != "" {
			return base, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".openhands"), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".openhands"), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
