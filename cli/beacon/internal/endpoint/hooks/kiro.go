package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kiro registers hooks as standalone JSON files in a directory it scans -- `.kiro/hooks/` in a
// project, `~/.kiro/hooks/` globally -- rather than in one file everybody shares. That makes this
// the simplest installer of the set: Beacon writes one file of its own and never reads, rewrites
// or deletes anything a user put beside it.
//
// It is worth naming why that is different from the two neighbouring shapes. OpenHands hands
// Beacon a single hooks.json that is also where the user keeps theirs, so the installer there is a
// careful merge. Muse Code has a settings key naming one managed file, so Beacon owning that file
// means taking the only slot, and the installer refuses when somebody already holds it. Kiro has
// neither problem: every file in the directory is loaded, so an extra one is additive by
// construction and there is no slot to contend for.
//
// Two things about the emitted file are load-bearing and neither is obvious from the schema:
//
//   - No `matcher`. The field is documented as a regex, and what it matches depends on the
//     trigger: a tool name on PreToolUse/PostToolUse, the prompt text on UserPromptSubmit, nothing
//     at all on SessionStart and Stop. Omitting it means always-match, which is what a telemetry
//     hook wants on every one of those. Writing the `"*"` that reads as "all tools" in Kiro's IDE
//     tool-name field would be a different thing entirely here -- `*` is not a valid regex, so at
//     best it matches nothing and at worst it fails to compile and takes the hook with it.
//
//   - No top-level key but `version` and `hooks`. Those two are the whole documented schema, and
//     Beacon's marker goes on each hook's `name` and `description` instead, which are documented
//     hook fields. Detection does not rely on either: the hook command is what identifies a Beacon
//     hook, the same evidence every other runtime's detection uses, because a name is something a
//     user can edit.

const (
	// kiroHookDirName is the directory Kiro scans, relative to the scope root.
	kiroHookDirName = "hooks"

	// kiroHookFileName is the file Beacon owns. Kebab-case and descriptive, which is what Kiro's
	// own guidance asks for; the name has no meaning to the loader, which takes every .json in the
	// directory.
	kiroHookFileName = "beacon-endpoint.json"

	// kiroSchemaVersion is the only value the `version` field accepts today. Written literally
	// rather than derived, because a file whose version Kiro does not recognize is not a file with
	// a warning -- it is hooks that never run.
	kiroSchemaVersion = "v1"

	// kiroHookName marks Beacon's hook definitions for a human reading the file. `name` is a
	// required field, so this costs nothing; it is deliberately not what detection matches on.
	kiroHookName = "beacon-endpoint-telemetry"

	// kiroHookDescription is the `description` field, which Kiro documents as documentation only.
	kiroHookDescription = "Beacon endpoint telemetry. Records local agent activity; makes no network calls."
)

// Kiro hook timeouts are seconds, and the runtime's own default is 60.
//
// Stated at the value for the reason the Qwen, Muse and OpenHands constants give: the same
// `timeout` field means milliseconds on Qwen and seconds here, the files look alike, and getting
// it wrong kills every hook mid-write while the install still reports success. Explicit values
// rather than the inherited default because 60 seconds is a long time to hold an agent turn for a
// telemetry hook that finishes in milliseconds -- these are ceilings on a hang, not budgets.
//
// The two long ones are long for a reason: prompt submission may run the inventory heartbeat, and
// the closing event flushes cloud telemetry, which has a ten-second timeout of its own.
//
// Zero is deliberately never written. On Kiro `0` does not mean "the default", it means *no
// timeout at all* -- a hook that hung would hang the agent turn with it, indefinitely.
const (
	kiroSessionStartTimeoutSeconds = 10
	kiroPromptSubmitTimeoutSeconds = 30
	kiroToolTimeoutSeconds         = 10
	kiroStopTimeoutSeconds         = 45
)

// kiroEvent binds one Kiro trigger to the beacon-hooks subcommand that maps it.
type kiroEvent struct {
	trigger    string
	subcommand string
	timeout    int
}

// kiroEvents are the triggers Beacon subscribes to.
//
// Five of Kiro's eleven, and the six that are missing are missing on purpose:
//
//   - PostFileCreate, PostFileSave and PostFileDelete would duplicate PostToolUse rather than add
//     to it. Kiro's file triggers fire only for changes the agent made, and every change the agent
//     makes goes through a write tool -- so subscribing would record two events for one write,
//     with no way for a reader to tell they were the same action. The duplicate would also be the
//     poorer of the two: a file trigger offers the path through a `{{filePath}}` template, while
//     PostToolUse carries the path, the operation, the content and the result. Double-counting a
//     file change in a security log is worse than the coverage this leaves on the table, which is
//     none.
//
//   - PreTaskExec and PostTaskExec announce a spec task starting and finishing. They are real
//     signal with no duplicate, and they are left out because Kiro does not document what they put
//     on stdin: without a task id or a task name the event would say that some task began, which
//     is not something an investigator can act on. This is the one deliberate gap that a published
//     payload would close, and it is recorded in the runtime docs as such rather than left for
//     someone to rediscover.
//
//   - Manual is an operator running a hook by hand. There is nothing for Beacon to observe in it.
//
// SessionEnd has no entry because Kiro has no such trigger: Stop is the closing event, and it
// fires per turn rather than per session.
var kiroEvents = []kiroEvent{
	{"SessionStart", "session-start", kiroSessionStartTimeoutSeconds},
	{"UserPromptSubmit", "prompt-submit", kiroPromptSubmitTimeoutSeconds},
	{"PreToolUse", "pre-tool", kiroToolTimeoutSeconds},
	{"PostToolUse", "post-tool", kiroToolTimeoutSeconds},
	{"Stop", "stop", kiroStopTimeoutSeconds},
}

type KiroOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type KiroStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

// kiroHooksFile is the v1 hook file, typed to exactly the fields Kiro documents.
//
// Its own types rather than the shared settingsHook* ones, for the reason the Muse types give: a
// field added to a shared struct for another runtime would silently change what Beacon emits here,
// and Kiro's rejection behavior for an unknown key is not documented. Types that cannot express a
// key Kiro has not published are what stops that.
type kiroHooksFile struct {
	Version string         `json:"version"`
	Hooks   []kiroHookSpec `json:"hooks"`
}

// kiroHookSpec is one hook definition.
//
// `matcher`, `confirm` and `enabled: false` are all absent by construction. The first two are
// explained on the file comment and on kiroEvents; `enabled` is written as an explicit true so the
// file says what it does rather than relying on the default, which is the same call the Muse
// installer makes about its schema version.
type kiroHookSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Trigger     string         `json:"trigger"`
	Action      kiroHookAction `json:"action"`
	Timeout     int            `json:"timeout,omitempty"`
	Enabled     bool           `json:"enabled"`
}

// kiroHookAction is a command action. The `agent` action type -- which injects a prompt instead of
// running a command -- is never written: it would spend model tokens and cannot observe anything.
type kiroHookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

var kiroRuntime = hookRuntime{
	displayName: "Kiro",
	configPath:  kiroHooksPath,
	install:     installKiroHooks,
	uninstall:   removeKiroHooks,
	isInstalled: isKiroInstalledAt,
}

func InstallKiro(opts KiroOptions) (KiroStatus, error) {
	status, err := installRuntimeHooks(kiroRuntime, RuntimeOptions(opts))
	if err != nil {
		return KiroStatus{}, err
	}
	return kiroStatusFromRuntime(status), nil
}

func UninstallKiro(opts KiroOptions) (KiroStatus, error) {
	status, err := uninstallRuntimeHooks(kiroRuntime, RuntimeOptions(opts))
	if err != nil {
		return KiroStatus{}, err
	}
	return kiroStatusFromRuntime(status), nil
}

func KiroHookStatus(opts KiroOptions) KiroStatus {
	return kiroStatusFromRuntime(runtimeHookStatus(kiroRuntime, RuntimeOptions(opts)))
}

func IsKiroInstalled(opts KiroOptions) bool {
	return isRuntimeInstalled(kiroRuntime, RuntimeOptions(opts))
}

func kiroStatusFromRuntime(status runtimeStatus) KiroStatus {
	return KiroStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

// installKiroHooks writes Beacon's hook file, replacing any earlier version of it wholesale.
//
// Wholesale rather than merged, because this file is Beacon's: nothing a user wrote lives in it,
// and their own hooks are separate files in the same directory that this never touches. Rewriting
// is also what lets an install drop a trigger Beacon no longer subscribes to, which a merge would
// leave behind pointing at a subcommand that had been removed.
func installKiroHooks(path, binaryPath, logPath, configPath string) error {
	// Refused rather than overwritten if the file is somebody else's. The name is distinctive
	// enough that a collision is unlikely, and "unlikely" is not a reason to delete a stranger's
	// hooks: Kiro loads every file in the directory, so theirs is live.
	if err := kiroHookFileIsBeacons(path); err != nil {
		return err
	}
	prefix := endpointCommandPrefix("kiro", binaryPath, logPath, configPath)
	file := kiroHooksFile{Version: kiroSchemaVersion}
	for _, event := range kiroEvents {
		file.Hooks = append(file.Hooks, kiroHookSpec{
			// The trigger is in the name because Kiro documents `name` as a unique identifier and
			// all five of these live in one file.
			Name:        kiroHookName + "-" + strings.ToLower(event.trigger),
			Description: kiroHookDescription,
			Trigger:     event.trigger,
			Action:      kiroHookAction{Type: "command", Command: prefix + " " + event.subcommand},
			Timeout:     event.timeout,
			Enabled:     true,
		})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// 0644 rather than the 0600 the settings-file installers use, for the reason the OpenHands
	// installer gives: a project-scope file gets committed and read by everyone working in the
	// repository, and a mode only its author can read would make the hooks stop working for the
	// next person to check it out. The same mode is used at user scope so the two scopes do not
	// differ in a way nobody would think to look for.
	return os.WriteFile(path, data, 0644)
}

// kiroHookFileIsBeacons reports whether Beacon may write the file at path.
//
// It may when there is nothing there, when the file is empty, when it is unreadable as a v1 hook
// file (which Kiro would be ignoring anyway), or when every hook in it is one Beacon wrote.
// Anything else is somebody's live registration under Beacon's chosen filename, and this returns
// an error naming it.
func kiroHookFileIsBeacons(path string) error {
	file, err := readKiroHooks(path)
	if err != nil {
		// A file Beacon cannot parse is one Kiro cannot load either, so there are no live hooks in
		// it to protect. Overwriting is the outcome that leaves the operator with working
		// telemetry rather than an install that refuses over a file that was already broken.
		return nil
	}
	for _, hook := range file.Hooks {
		if !isEndpointHookCommand(hook.Action.Command, "kiro") {
			return fmt.Errorf(
				"%s already defines a hook Beacon did not write (%q); Beacon owns that filename, "+
					"so it will not overwrite it. Move that hook to another file in the same "+
					"directory -- Kiro merges every hook file it finds -- and run the install again",
				path, hook.Name)
		}
	}
	return nil
}

// readKiroHooks parses a hook file, or returns an empty one when there is none.
func readKiroHooks(path string) (kiroHooksFile, error) {
	var file kiroHooksFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return kiroHooksFile{}, err
	}
	// An empty file is not malformed JSON to a person, and treating it as an error would make an
	// install fail on a file somebody had created but not written.
	if len(strings.TrimSpace(string(data))) == 0 {
		return file, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return kiroHooksFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	return file, nil
}

// removeKiroHooks deletes Beacon's hook file.
//
// The whole file, because the whole file is Beacon's. A file that has since acquired a hook Beacon
// did not write is left alone rather than deleted: an uninstall may remove what it added and
// nothing more, and the same check that refuses to overwrite a stranger's file refuses to delete
// one.
func removeKiroHooks(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	if err := kiroHookFileIsBeacons(path); err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// isKiroInstalledAt reports whether Beacon's hooks are registered at path.
//
// By the hook command, not by the file existing. A file left behind from a previous version of
// Beacon, or one truncated by a failed write, exists and registers nothing -- and reporting that
// as installed is how an operator ends up believing a machine is monitored when it is not.
func isKiroInstalledAt(path string) bool {
	file, err := readKiroHooks(path)
	if err != nil {
		return false
	}
	for _, hook := range file.Hooks {
		if isEndpointHookCommand(hook.Action.Command, "kiro") {
			return true
		}
	}
	return false
}

func kiroHooksPath(level Level) (string, error) {
	dir, err := kiroHooksDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, kiroHookFileName), nil
}

// kiroHooksDir resolves the hooks directory for a scope.
//
// Both scopes are real and, unusually, neither shadows the other: Kiro documents hooks as merged
// across scopes rather than resolved by precedence, so a user-scope install keeps working inside a
// repository that has hooks of its own. That is the opposite of OpenHands, where the loader takes
// the first location that exists and a project file hides the user one entirely -- and it is why
// this installer has no advice to give about which scope to pick. User scope covers every project
// on the machine, which is what an endpoint agent usually wants; project scope commits the hook
// with the repository, which is what a team standardizing on Beacon usually wants; and having both
// is not a conflict.
//
// KIRO_HOME takes precedence at user scope, matching how Kiro resolves it. It exists so a person
// can keep separate Kiro profiles on one machine, and on a machine that sets it, ~/.kiro is not
// where Kiro looks -- so an install that ignored it would write a file nothing reads.
func kiroHooksDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if base := strings.TrimSpace(os.Getenv("KIRO_HOME")); base != "" {
			return filepath.Join(base, kiroHookDirName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".kiro", kiroHookDirName), nil
	case LevelProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".kiro", kiroHookDirName), nil
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
