package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeepSeek Harness is the one supported runtime where installing hooks takes two files, because
// the runtime does not read a hooks file until something tells it to.
//
// `dsh` runs Claude Code hooks through `@deepseek-ai/dsh-hooks-claude-code`, a bridge DeepSeek
// ships as a dependency of the CLI but does not mount. Nothing is loaded by convention: the
// harness composes itself from a plugin tree, and a plugin that is not in the tree does not exist.
// So Beacon writes
//
//  1. `$DSH_HOME/beacon-endpoint-hooks.json` -- a Claude Code hooks file Beacon owns outright, and
//  2. one row in `$DSH_HOME/cordis.patch.yml` mounting the bridge at that file.
//
// The second file is the interesting one and it is not Beacon's. It is the user's own tweak layer,
// applied over every profile, and it may already carry their rows, their comments and `!!js`
// expressions evaluated at boot. So it is edited as a document rather than rewritten: parsed into
// a yaml.Node, one element appended or removed, re-encoded. yaml.v3 preserves comments, quoting
// style and custom tags through that round trip, which is what makes editing a live user file
// defensible at all.
//
// Two properties of that file are load-bearing and neither is obvious:
//
//   - An empty or comments-only patch file FAILS BOOT. It is not ignored and it is not a warning:
//     `dsh` refuses to start. So an uninstall that removes Beacon's last row must remove the file,
//     not truncate it -- an absent patch layer is fine, an empty one is not.
//
//   - The home-level file outranks the per-profile one and applies to every profile. That is why
//     Beacon installs here rather than into `profiles/web/cordis.patch.yml`: one install covers the
//     CLI, Web, ACP and SDK surfaces, which are four compositions of one harness rather than four
//     products.
//
// There is no project scope, and its absence is a property of the runtime rather than a decision
// here. The bridge's `configPath` is process-level -- read once at startup, resolved against the
// launch directory -- and the bridge's own source records per-session discovery of a project-local
// hooks file as unimplemented. There is nowhere for a repository to put one, so dshHooksPath
// refuses the level with that explanation instead of writing a file nothing would read.
//
// Beacon owns its filename in the first file and its row id in the second, and refuses rather than
// overwriting anything it did not write.

const (
	// dshHookFileName is the Claude Code hooks file Beacon owns. It sits beside cordis.patch.yml
	// in the Harness home, which is also how the patch path is derived from it.
	dshHookFileName = "beacon-endpoint-hooks.json"

	// dshPatchFileName is the home-level user patch layer, applied over every profile. The name is
	// the runtime's, not Beacon's choice.
	dshPatchFileName = "cordis.patch.yml"

	// dshBridgePackage is the plugin that runs Claude Code hooks against the harness's extension
	// points. It ships as a dependency of the `dsh` CLI, so mounting it needs no install step --
	// the row alone activates it, the same way the runtime's own webhook overlay works.
	dshBridgePackage = "@deepseek-ai/dsh-hooks-claude-code"

	// dshPatchEntryID identifies Beacon's row.
	//
	// Unlike every other runtime's detection, this is matched on an id rather than on the hook
	// command -- the row carries no command, because the commands live in the hooks file it points
	// at. That is sound here rather than a compromise: the loader itself diffs entries by `id`, so
	// this string is load-bearing to the runtime and not a label a user would edit for cosmetic
	// reasons. The hook commands are still what `dshHasEndpointHooks` checks in the other file, so
	// an install is only reported complete when both halves are.
	dshPatchEntryID = "beacon-endpoint-hooks"

	// dshEnvHome is the environment variable that moves the Harness home. On a machine that sets
	// it, ~/.dsh is not where dsh looks, so an install that ignored it would write two files
	// nothing reads.
	dshEnvHome = "DSH_HOME"

	// dshHomeDirName is the default Harness home under the user's home directory.
	dshHomeDirName = ".dsh"
)

// dsh hook timeouts are seconds -- the bridge reads a hook's `timeout` as `timeoutSec`, matching
// Claude Code, and applies 600 seconds when one is absent.
//
// Stated at the value for the reason the Kiro, Muse, Qwen and OpenHands constants give: the same
// `timeout` field means milliseconds on some runtimes and seconds here, the files look alike, and
// getting it wrong kills every hook mid-write while the install still reports success. Explicit
// values rather than the inherited default because ten minutes is a very long time to hold an
// agent turn for a telemetry hook that finishes in milliseconds -- these are ceilings on a hang,
// not budgets.
//
// The two long ones are long for a reason: the closing event flushes cloud telemetry, which has a
// ten-second timeout of its own, and prompt submission does the heavier session bookkeeping.
const (
	dshSessionStartTimeoutSeconds = 10
	dshPromptSubmitTimeoutSeconds = 30
	dshToolTimeoutSeconds         = 10
	dshStopTimeoutSeconds         = 45
	dshSubagentTimeoutSeconds     = 10
)

// dshEvent binds one bridge event to the beacon-hooks subcommand that maps it.
type dshEvent struct {
	name       string
	subcommand string
	timeout    int
}

// dshEvents are every event the bridge supports.
//
// All seven, which is unusual -- most installers leave something out. Here there is nothing to
// leave out: the bridge supports exactly these, and each one maps onto a subcommand Beacon already
// has. The 23 Claude Code events it does not support are not a choice Beacon makes; config for
// them is discarded before the bridge's parser sees it, so writing them would be writing
// instructions that are silently dropped.
//
// Stop is per turn rather than per session, and there is no SessionEnd on this runtime, so it is
// the closing event -- the same shape as Kiro.
var dshEvents = []dshEvent{
	{"SessionStart", "session-start", dshSessionStartTimeoutSeconds},
	{"UserPromptSubmit", "prompt-submit", dshPromptSubmitTimeoutSeconds},
	{"PreToolUse", "pre-tool", dshToolTimeoutSeconds},
	{"PostToolUse", "post-tool", dshToolTimeoutSeconds},
	{"Stop", "stop", dshStopTimeoutSeconds},
	{"SubagentStart", "subagent-start", dshSubagentTimeoutSeconds},
	{"SubagentStop", "subagent-stop", dshSubagentTimeoutSeconds},
}

type DshOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type DshStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	PatchPath  string `json:"patch_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

// dshHooksFile is the Claude Code hooks document Beacon writes: a bare event map.
//
// Bare rather than wrapped in a `hooks` key. The bridge accepts either -- it reads `root.hooks` and
// falls back to the root itself -- and the bare form is unambiguous here because every key is an
// event name. It is also its own type rather than the shared settingsHook* ones, for the reason
// the Muse and Kiro types give: a field added to a shared struct for another runtime would silently
// change what Beacon emits here.
type dshHooksFile map[string][]dshHookGroup

// dshHookGroup is one matcher group. `matcher` is absent by construction.
//
// Absent means match-all in this dialect, which is what a telemetry hook wants on every event. It
// is not written as `"*"` even though that is also a match-all sentinel: the bridge validates a
// matcher at parse time and rejects the whole config on an invalid one, so the value that needs no
// validating is the safer one to write.
type dshHookGroup struct {
	Hooks []dshHookRef `json:"hooks"`
}

// dshHookRef is one command hook. `type` is always "command": the bridge runs only command
// handlers and returns every other type as skipped, with a warning and no hook registered.
type dshHookRef struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

var dshRuntime = hookRuntime{
	displayName: "DeepSeek Harness",
	configPath:  dshHooksPath,
	install:     installDshHooks,
	uninstall:   removeDshHooks,
	isInstalled: isDshInstalledAt,
}

func InstallDsh(opts DshOptions) (DshStatus, error) {
	status, err := installRuntimeHooks(dshRuntime, RuntimeOptions(opts))
	if err != nil {
		return DshStatus{}, err
	}
	return dshStatusFromRuntime(status), nil
}

func UninstallDsh(opts DshOptions) (DshStatus, error) {
	status, err := uninstallRuntimeHooks(dshRuntime, RuntimeOptions(opts))
	if err != nil {
		return DshStatus{}, err
	}
	return dshStatusFromRuntime(status), nil
}

func DshHookStatus(opts DshOptions) DshStatus {
	return dshStatusFromRuntime(runtimeHookStatus(dshRuntime, RuntimeOptions(opts)))
}

func IsDshInstalled(opts DshOptions) bool {
	return isRuntimeInstalled(dshRuntime, RuntimeOptions(opts))
}

func dshStatusFromRuntime(status runtimeStatus) DshStatus {
	dsh := DshStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
	// The patch path is reported alongside, because it is half the install and the half an
	// operator is least likely to guess. A status naming only the hooks file would send someone
	// looking for a mount that is not there.
	if status.ConfigPath != "" {
		dsh.PatchPath = dshPatchPathFor(status.ConfigPath)
	}
	return dsh
}

// dshPatchPathFor resolves the patch file that belongs to a hooks file.
//
// A sibling, because both live directly in the Harness home. Derived rather than resolved
// independently so the two halves of an install cannot end up in different directories when
// DSH_HOME changes between calls -- and so the shared hookRuntime plumbing, which carries one path,
// still reaches both.
func dshPatchPathFor(hooksPath string) string {
	return filepath.Join(filepath.Dir(hooksPath), dshPatchFileName)
}

// installDshHooks writes Beacon's hooks file and mounts the bridge at it.
//
// The hooks file is rewritten wholesale because it is Beacon's: nothing a user wrote lives in it.
// Rewriting is also what lets an install drop an event Beacon no longer subscribes to, which a
// merge would leave behind pointing at a subcommand that had been removed.
//
// The patch file is merged, because it is not.
//
// Order matters and is deliberate: the hooks file first, the mount second. Between the two steps
// the machine has a hooks file nothing reads, which is inert. The other order leaves a mount
// pointing at a file that does not exist, and the bridge's answer to that is to log a warning and
// register nothing -- an install that looks complete and captures nothing.
func installDshHooks(path, binaryPath, logPath, configPath string) error {
	if err := dshHookFileIsBeacons(path); err != nil {
		return err
	}
	prefix := endpointCommandPrefix("dsh", binaryPath, logPath, configPath)
	file := dshHooksFile{}
	for _, event := range dshEvents {
		file[event.name] = []dshHookGroup{{Hooks: []dshHookRef{{
			Type:    "command",
			Command: prefix + " " + event.subcommand,
			Timeout: event.timeout,
		}}}}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// 0644 rather than 0600. The Harness home is the user's own, but dsh may be launched by a
	// service or a desktop host running as a different account, and a file only its author can
	// read would make the hooks stop working there with no diagnostic -- the bridge logs a warning
	// and registers nothing. The file contains a command line and no secrets.
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	return addDshPatchEntry(dshPatchPathFor(path), path)
}

// removeDshHooks removes both halves, and reports whether anything was there.
//
// The mount first, the hooks file second -- the reverse of install, for the same reason. Between
// the two steps there is a hooks file nothing reads; the other order would leave a live mount
// pointing at a file that had just been deleted.
//
// A hooks file that has since acquired a hook Beacon did not write is left alone rather than
// deleted: an uninstall may remove what it added and nothing more.
func removeDshHooks(path string) (bool, error) {
	patchRemoved, err := removeDshPatchEntry(dshPatchPathFor(path))
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return patchRemoved, nil
	}
	if err := dshHookFileIsBeacons(path); err != nil {
		return patchRemoved, nil
	}
	if err := os.Remove(path); err != nil {
		return patchRemoved, err
	}
	return true, nil
}

// isDshInstalledAt reports whether telemetry will actually flow.
//
// Both halves, and the conjunction is the point. A hooks file alone is a file nothing reads; a
// mount alone is a bridge that logs a warning and registers nothing. Reporting either one as
// installed is how an operator ends up believing a machine is monitored when it is not -- which is
// the same failure the other installers guard against by checking the hook command rather than the
// file's existence, applied to a runtime where the evidence is split across two files.
func isDshInstalledAt(path string) bool {
	return dshHasEndpointHooks(path) && dshPatchHasEntry(dshPatchPathFor(path))
}

// dshHasEndpointHooks reports whether the hooks file registers a Beacon command.
//
// By the command, not by the file existing, for the reason every other installer gives: a file
// left behind by an earlier version, or truncated by a failed write, exists and registers nothing.
func dshHasEndpointHooks(path string) bool {
	file, err := readDshHooks(path)
	if err != nil {
		return false
	}
	for _, groups := range file {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if isEndpointHookCommand(hook.Command, "dsh") {
					return true
				}
			}
		}
	}
	return false
}

// dshHookFileIsBeacons reports whether Beacon may write the hooks file at path.
//
// It may when there is nothing there, when the file is empty, when it is unreadable as a hooks
// document (which the bridge would be refusing to load anyway), or when every hook in it is one
// Beacon wrote. Anything else is somebody's live registration under Beacon's chosen filename.
func dshHookFileIsBeacons(path string) error {
	file, err := readDshHooks(path)
	if err != nil {
		// A file Beacon cannot parse is one the bridge cannot load either -- it logs a warning and
		// registers no hooks at all -- so there is nothing live in it to protect. Overwriting
		// leaves the operator with working telemetry rather than an install that refuses over a
		// file that was already broken.
		return nil
	}
	for event, groups := range file {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if !isEndpointHookCommand(hook.Command, "dsh") {
					return fmt.Errorf(
						"%s already defines a %s hook Beacon did not write (%q); Beacon owns that "+
							"filename, so it will not overwrite it. Point the bridge at your own "+
							"hooks file under a different name and run the install again",
						path, event, hook.Command)
				}
			}
		}
	}
	return nil
}

// readDshHooks parses the hooks file, or returns an empty one when there is none.
func readDshHooks(path string) (dshHooksFile, error) {
	file := dshHooksFile{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return nil, err
	}
	// An empty file is not malformed JSON to a person, and treating it as an error would make an
	// install fail on a file somebody had created but not written.
	if len(strings.TrimSpace(string(data))) == 0 {
		return file, nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return file, nil
}

func dshHooksPath(level Level) (string, error) {
	dir, err := dshHomeDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, dshHookFileName), nil
}

// dshHomeDir resolves the Harness home for a scope.
//
// User scope only, and the project case is an error with its reason rather than a silent fallback
// to user scope. Falling back would be worse than refusing: an operator asking for a repository-
// scoped install would get a machine-wide one and no indication that is what happened.
func dshHomeDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if base := strings.TrimSpace(os.Getenv(dshEnvHome)); base != "" {
			return base, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, dshHomeDirName), nil
	case LevelProject:
		return "", fmt.Errorf(
			"DeepSeek Harness has no project-scoped hook configuration: the hook bridge reads one " +
				"process-level config chosen by the plugin tree, and the plugin tree is composed " +
				"from the Harness home. Install at user scope, which covers every profile and " +
				"every project on this machine")
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
