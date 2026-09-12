package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// goose discovers hooks through plugins, not through a settings file, so Beacon installs itself as
// a plugin whose only content is a hooks file.
//
// The mechanism, from crates/goose/src/plugins/discovery.rs and crates/goose/src/hooks/mod.rs:
// goose lists the immediate subdirectories of its plugins directory, treats each one as a plugin
// named after the directory, and loads `<plugin-root>/hooks/hooks.json` from every plugin that is
// enabled. So Beacon owns a directory rather than a file -- the Kiro shape one level up.
//
// Three consequences of that shape, all of which this installer is built around:
//
//   - The plugins directory is `.agents/plugins`, which is the Open Plugins convention rather than
//     goose's own namespace. Another agent that implements the same specification would discover
//     this plugin too and run its commands, and the command says `--platform goose`, so those
//     events would be attributed to goose. Beacon has no way to tell from a payload which host
//     invoked it -- goose sets no distinguishing variable, and the event schema carries no field
//     for the question. This is a real limitation with no fix available on Beacon's side; it is
//     recorded here, and in the README, rather than papered over. It costs nothing today, because
//     goose is the only runtime Beacon supports that reads this directory.
//   - Plugin names are deduplicated across scopes, project before user (discover_enabled_plugins
//     sorts by scope and keeps the first of each name). So a project install of this plugin
//     shadows the user one rather than adding to it, which is why the two write the same directory
//     name deliberately: an operator who installs at both scopes gets one registration, not two
//     copies of every event.
//   - An operator can disable the plugin without touching this file, through `plugins` in
//     config.yaml or `disabledPlugins` in settings.json. A disabled plugin still has its hooks
//     file on disk, so "the file is there" is not the same as "the hooks run" -- see
//     GooseHookStatus.
//
// What goose does with a badly-shaped file is the reason both the emitted shape and the payload
// readers are pinned by tests: an unparseable hooks.json is skipped with a warn line, an unknown
// event name is ignored silently, an unsupported action type is ignored silently, and an invalid
// matcher regex drops just that rule. Nothing surfaces to the operator.

// gooseHookDirName and gooseHookFileName are the path goose loads hooks from inside a plugin.
const (
	gooseHookDirName  = "hooks"
	gooseHookFileName = "hooks.json"
)

// goosePluginDirName is the directory Beacon owns inside the plugins directory.
//
// It is also the plugin's name, because goose names a plugin after its directory. `beacon-endpoint`
// rather than `beacon` so that the name says what it is in a listing of plugins an operator did not
// all install themselves, and so that it does not collide with anything a person might create while
// experimenting.
const goosePluginDirName = "beacon-endpoint"

// gooseAgentsDirName and goosePluginsDirName spell the Open Plugins layout goose discovers.
const (
	gooseAgentsDirName  = ".agents"
	goosePluginsDirName = "plugins"
)

// goosePathRootEnv is goose's override for every one of its directories.
//
// It is honored for the same reason the Kiro installer honors KIRO_HOME: on a machine that sets it,
// the default location is not where goose looks, so an install that ignored it would write a file
// nothing reads. goose requires it to be absolute and ignores it otherwise, and this matches that
// -- a relative value resolves to the default rather than to a path under the working directory.
const goosePathRootEnv = "GOOSE_PATH_ROOT"

// goose hook timeouts are seconds, and goose's own default is 30.
//
// Stated at every value rather than inherited, for the reason the Qwen, Muse, OpenHands and Kiro
// constants give: the same `timeout` field means milliseconds on Qwen and seconds here, and the
// files look alike. These are ceilings on a hang rather than budgets -- the hooks finish in
// milliseconds.
//
// The two long ones are long for a reason. Prompt submission may run the inventory heartbeat, and
// the closing events flush cloud telemetry, which has a ten-second timeout of its own. Both are
// worth noting on this runtime specifically, because goose holds the turn for a blocking hook and
// `Stop` is one of them: a hung stop hook is a hung turn until this timeout expires.
const (
	gooseSessionStartTimeoutSeconds = 10
	goosePromptSubmitTimeoutSeconds = 30
	gooseToolTimeoutSeconds         = 10
	gooseStopTimeoutSeconds         = 45
	gooseSessionEndTimeoutSeconds   = 45
)

// gooseOnFailureAllow is written on the one event where goose reads the field.
//
// `on_failure` is honored only for PreToolUse, and "allow" is already goose's default -- so this
// changes nothing today and is written anyway, because the thing it guards against is goose
// changing that default. A telemetry hook that blocked a tool call because it had itself failed
// would be turning an observation problem into an availability problem on the operator's machine.
// Saying so in the file means the answer does not depend on a default Beacon does not control.
const gooseOnFailureAllow = "allow"

// gooseEvent binds one goose lifecycle event to the beacon-hooks subcommand that maps it.
type gooseEvent struct {
	name       string
	subcommand string
	timeout    int
}

// gooseEvents are the seven events Beacon subscribes to, out of the twelve goose exposes.
//
// PostToolUse and PostToolUseFailure share the `post-tool` subcommand. They can, because their
// payloads are identical, and they must be separate rows because goose has no single post-tool
// event -- the mapper tells them apart by reading `event`.
//
// The five that are left out, and why each is left out rather than forgotten:
//
//   - BeforeShellExecution, AfterShellExecution, BeforeReadFile and AfterFileEdit are duplicates.
//     goose fires each one immediately alongside PreToolUse or PostToolUse for the same call,
//     carrying the same tool_name and the same tool_input, with only matcher_context differing --
//     and, unlike the events they shadow, carrying no tool_call_id at all. Subscribing would record
//     every shell command and every file edit twice, and the second copy would be the one that
//     could not be correlated with anything.
//   - PreToolUseResult reports what the PreToolUse hook chain decided: allow or deny, which plugin
//     denied, and why. That is a real signal and not a duplicate, but it fires once per tool call
//     whether or not anything was denied, so subscribing to it as-is would double the event volume
//     to record an outcome that is "allow" almost every time. It is deliberately deferred rather
//     than declined.
var gooseEvents = []gooseEvent{
	{"SessionStart", "session-start", gooseSessionStartTimeoutSeconds},
	{"UserPromptSubmit", "prompt-submit", goosePromptSubmitTimeoutSeconds},
	{"PreToolUse", "pre-tool", gooseToolTimeoutSeconds},
	{"PostToolUse", "post-tool", gooseToolTimeoutSeconds},
	{"PostToolUseFailure", "post-tool", gooseToolTimeoutSeconds},
	{"Stop", "stop", gooseStopTimeoutSeconds},
	{"SessionEnd", "session-end", gooseSessionEndTimeoutSeconds},
}

type GooseOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type GooseStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

// gooseHooksFile is the document goose parses, typed to exactly the fields it reads.
//
// Its own types rather than the shared settingsHook* ones, for the reason the Muse and Kiro types
// give: a field added to a shared struct for another runtime would silently change what Beacon
// emits here. goose ignores keys it does not know rather than rejecting them, so a stray field
// would not break the file -- it would sit in an operator's plugin directory meaning nothing,
// which is its own kind of wrong.
type gooseHooksFile struct {
	Hooks map[string][]gooseHookRule `json:"hooks"`
}

// gooseHookRule is one matcher group: the rules that fire for an event.
//
// `matcher` is deliberately absent rather than written as a catch-all. goose tests the regex
// against matcher_context, and matcher_context is not a tool name on every event -- on
// UserPromptSubmit it is the *prompt text*, because HookContext::with_message sets it. A matcher
// that looked like a harmless ".*" would therefore be a regex run over every prompt, and any
// narrower one would silently drop prompts that did not match it. An omitted matcher always fires,
// which is what an observing hook wants on all seven events.
type gooseHookRule struct {
	Hooks []gooseHookAction `json:"hooks"`
}

// gooseHookAction is a command action, holding only the fields Beacon writes.
//
// goose supports no other action type today and ignores the ones it does not recognize, so `type`
// is written explicitly rather than relying on the null-means-command default: a file that says
// what it is survives goose gaining a second action type whose absence of a `type` means something
// else.
type gooseHookAction struct {
	Type      string `json:"type"`
	Command   string `json:"command"`
	Timeout   int    `json:"timeout,omitempty"`
	OnFailure string `json:"on_failure,omitempty"`
}

var gooseRuntime = hookRuntime{
	displayName: "goose",
	configPath:  gooseHooksPath,
	install:     installGooseHooks,
	uninstall:   removeGooseHooks,
	isInstalled: isGooseInstalledAt,
}

func InstallGoose(opts GooseOptions) (GooseStatus, error) {
	status, err := installRuntimeHooks(gooseRuntime, RuntimeOptions(opts))
	if err != nil {
		return GooseStatus{}, err
	}
	return gooseStatusFromRuntime(status), nil
}

func UninstallGoose(opts GooseOptions) (GooseStatus, error) {
	status, err := uninstallRuntimeHooks(gooseRuntime, RuntimeOptions(opts))
	if err != nil {
		return GooseStatus{}, err
	}
	return gooseStatusFromRuntime(status), nil
}

func GooseHookStatus(opts GooseOptions) GooseStatus {
	return gooseStatusFromRuntime(runtimeHookStatus(gooseRuntime, RuntimeOptions(opts)))
}

func IsGooseInstalled(opts GooseOptions) bool {
	return isRuntimeInstalled(gooseRuntime, RuntimeOptions(opts))
}

func gooseStatusFromRuntime(status runtimeStatus) GooseStatus {
	return GooseStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

// installGooseHooks writes Beacon's hooks file, replacing any earlier version of it wholesale.
//
// Wholesale rather than merged, because the file is Beacon's: it sits inside a plugin directory
// Beacon created, and an operator's own hooks live in their own plugin. Rewriting is also what lets
// an install drop an event Beacon no longer subscribes to, which a merge would leave behind
// pointing at a subcommand that had been removed.
func installGooseHooks(path, binaryPath, logPath, configPath string) error {
	// Refused rather than overwritten if the file registers something Beacon did not write. The
	// plugin directory name is distinctive, and "unlikely to collide" is not a reason to delete a
	// stranger's hooks: whatever is in this file is live for every goose session on the machine.
	if err := gooseHookFileIsBeacons(path); err != nil {
		return err
	}
	prefix := endpointCommandPrefix(gooseHookPlatform, binaryPath, logPath, configPath)
	file := gooseHooksFile{Hooks: map[string][]gooseHookRule{}}
	for _, event := range gooseEvents {
		action := gooseHookAction{
			Type:    "command",
			Command: prefix + " " + event.subcommand,
			Timeout: event.timeout,
		}
		// Only on the event where goose reads it. Writing it everywhere would put a field in the
		// file that goose silently ignores on six of seven events, which reads as a contract that
		// does not exist.
		if event.name == "PreToolUse" {
			action.OnFailure = gooseOnFailureAllow
		}
		// Appended rather than assigned, so that two events sharing a subcommand -- PostToolUse and
		// PostToolUseFailure -- stay two entries under two names rather than one overwriting the
		// other.
		file.Hooks[event.name] = append(file.Hooks[event.name], gooseHookRule{Hooks: []gooseHookAction{action}})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// 0644 rather than the 0600 the settings-file installers use, for the reason the OpenHands and
	// Kiro installers give: a project-scope plugin gets committed and read by everyone working in
	// the repository, and a mode only its author can read would make the hooks stop working for the
	// next person to check it out. The same mode at user scope so the two do not differ in a way
	// nobody would think to look for.
	return os.WriteFile(path, data, 0644)
}

// gooseHookPlatform is the `--platform` value written into every hook command, and the value the
// hook adapter keys its goose readers on.
const gooseHookPlatform = "goose"

// gooseHookFileIsBeacons reports whether Beacon may write the file at path.
//
// It may when there is nothing there, when the file is empty, when it is unreadable as a hooks file
// (which goose would be skipping anyway), or when every command in it is one Beacon wrote. Anything
// else is somebody's live registration inside Beacon's plugin directory, and this returns an error
// naming it.
func gooseHookFileIsBeacons(path string) error {
	file, err := readGooseHooks(path)
	if err != nil {
		// A file Beacon cannot parse is one goose cannot load either, so there are no live hooks in
		// it to protect. Overwriting leaves the operator with working telemetry rather than an
		// install that refuses over a file that was already broken.
		return nil
	}
	for event, rules := range file.Hooks {
		for _, rule := range rules {
			for _, action := range rule.Hooks {
				if strings.TrimSpace(action.Command) == "" {
					continue
				}
				if !isEndpointHookCommand(action.Command, gooseHookPlatform) {
					return fmt.Errorf(
						"%s already defines a %s hook Beacon did not write (%q); Beacon owns that "+
							"plugin directory, so it will not overwrite it. Move that hook into a "+
							"plugin of its own under the same plugins directory -- goose loads every "+
							"plugin it finds -- and run the install again",
						path, event, action.Command)
				}
			}
		}
	}
	return nil
}

// readGooseHooks parses a hooks file, or returns an empty one when there is none.
func readGooseHooks(path string) (gooseHooksFile, error) {
	var file gooseHooksFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return gooseHooksFile{}, err
	}
	// An empty file is not malformed JSON to a person, and treating it as an error would make an
	// install fail on a file somebody had created but not written.
	if len(strings.TrimSpace(string(data))) == 0 {
		return file, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return gooseHooksFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	return file, nil
}

// removeGooseHooks deletes Beacon's hooks file, and the plugin directory it created with it.
//
// The directory too, because a plugin directory with no hooks file is a plugin goose still
// discovers, still adds to the `plugins` map in config.yaml, and still lists -- an empty
// registration left behind by an uninstall. Removal is bounded to the two directories Beacon made:
// the `hooks` directory and the plugin root, each removed only if empty, so a directory an operator
// put something else in survives.
//
// A file that has since acquired a hook Beacon did not write is left alone rather than deleted: an
// uninstall may remove what it added and nothing more, and the same check that refuses to overwrite
// a stranger's file refuses to delete one.
func removeGooseHooks(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	if err := gooseHookFileIsBeacons(path); err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	// Errors are deliberately ignored: os.Remove on a non-empty directory fails, which is exactly
	// the case where the directory must stay. The hooks file is gone either way, which is what
	// decides whether goose runs anything.
	hooksDir := filepath.Dir(path)
	_ = os.Remove(hooksDir)
	_ = os.Remove(filepath.Dir(hooksDir))
	return true, nil
}

// isGooseInstalledAt reports whether Beacon's hooks are registered at path.
//
// By the hook command, not by the file existing. A file left behind by an older Beacon, or one
// truncated by a failed write, exists and registers nothing -- and reporting that as installed is
// how an operator ends up believing a machine is monitored when it is not.
//
// What this cannot see is whether the plugin is enabled. goose lets an operator disable a plugin
// from config.yaml's `plugins` map or from `disabledPlugins` in a settings.json at any of three
// scopes, and a disabled plugin keeps its hooks file. Reading all four files to answer would mean
// reimplementing goose's precedence rules against a format it does not version; reporting the
// registration is the narrower claim and the one this function can actually make.
func isGooseInstalledAt(path string) bool {
	file, err := readGooseHooks(path)
	if err != nil {
		return false
	}
	for _, rules := range file.Hooks {
		for _, rule := range rules {
			for _, action := range rule.Hooks {
				if isEndpointHookCommand(action.Command, gooseHookPlatform) {
					return true
				}
			}
		}
	}
	return false
}

func gooseHooksPath(level Level) (string, error) {
	root, err := goosePluginRoot(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, gooseHookDirName, gooseHookFileName), nil
}

// goosePluginRoot resolves Beacon's plugin directory for a scope.
//
// Both scopes are real and, unlike Kiro's, they do not add up: goose deduplicates plugins by name
// with project scope first, so a project install of this plugin replaces the user one for sessions
// in that repository rather than running alongside it. That is the behavior worth having -- one
// registration means one copy of every event -- and it is why both scopes write the same directory
// name rather than distinct ones.
//
// GOOSE_PATH_ROOT takes precedence at user scope, matching how goose resolves it, including the
// requirement that it be absolute: goose ignores a relative value, so honoring one here would write
// a file nothing reads. Project scope does not consult it, because goose's project lookup is
// always `<project>/.agents/plugins`.
func goosePluginRoot(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		base, err := gooseUserAgentsDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, goosePluginsDirName, goosePluginDirName), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, gooseAgentsDirName, goosePluginsDirName, goosePluginDirName), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}

// gooseUserAgentsDir resolves the `.agents` directory goose looks in at user scope.
func gooseUserAgentsDir() (string, error) {
	if root := strings.TrimSpace(os.Getenv(goosePathRootEnv)); filepath.IsAbs(root) {
		return filepath.Join(root, gooseAgentsDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, gooseAgentsDirName), nil
}
