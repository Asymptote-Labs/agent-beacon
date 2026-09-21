package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Kimi Code is the one supported runtime whose hook registration lives inside a file that also
// holds the user's API keys.
//
// There is one place to register: a `[[hooks]]` array of tables in `$KIMI_CODE_HOME/config.toml`,
// which is the same file that carries `[providers.<name>].api_key`, the model catalog, the
// permission rules and whatever comments the user wrote around them. Kimi Code has no
// project-level configuration at all -- its own documentation says the CLI reads a single
// user-level config file, and the way to isolate a project is to point KIMI_CODE_HOME somewhere
// else -- so there is no repository-scoped install to offer.
//
// Two consequences shape everything below.
//
// The first is that Beacon never re-serializes this file. It appends its `[[hooks]]` entries to
// the end of the text and leaves every byte above them exactly as it found them, so a secret
// cannot be rewritten, re-quoted, reordered or dropped by a round trip through a parser -- and a
// comment the user wrote next to one cannot be lost. Appending is sound rather than merely
// convenient: an array-of-tables header at the end of a TOML document opens a new table and
// cannot change the meaning of anything before it, which is true of TOML 1.0 generally and
// verified here against the exact parser Kimi Code uses. Every write is parsed back and compared
// against what it was supposed to produce before it replaces the file, so a scanning mistake
// fails the install instead of corrupting a config.
//
// The second is that detection and removal go by the hook command rather than by the comment
// Beacon writes above its block, and that is not the usual caution -- there is a specific,
// reachable case. Kimi Code's legacy migration from `kimi-cli` merges the old home's settings
// into this file by serializing the merged result, which preserves the `[[hooks]]` entries and
// drops every comment in the document. A marker-based uninstall would silently fail to find its
// own block on any machine that had been through that upgrade. The comment is a label for a
// person reading the file, the same way OpenHands' `name` field is, and nothing matches on it.
//
// One shape is refused rather than written into: a `hooks` key written as an inline array
// (`hooks = [{...}]`). TOML forbids appending `[[hooks]]` after that, so the file would stop
// loading -- and on this runtime a config that does not parse is not a warning, it is a startup
// failure. The refusal names the shape and what to do about it.

const (
	// kimiConfigFileName is the runtime's own name for its configuration. Beacon does not own
	// this file; it is a guest in it.
	kimiConfigFileName = "config.toml"

	// kimiHooksKey is the top-level array of tables hook rules live in.
	kimiHooksKey = "hooks"

	// kimiEnvHome is the environment variable that moves the whole Kimi Code data root --
	// config, sessions, credentials and logs. On a machine that sets it, ~/.kimi-code is not
	// where the runtime looks, so an install that ignored it would write into a file nothing
	// reads.
	kimiEnvHome = "KIMI_CODE_HOME"

	// kimiHomeDirName is the default data root under the user's home directory.
	kimiHomeDirName = ".kimi-code"
)

// kimiManagedComment labels Beacon's block for a person reading the file.
//
// Informational only. It is deliberately not what uninstall and status match on -- a user can
// edit it, and Kimi Code's own legacy migration rewrites this file in a way that drops every
// comment while keeping the hook entries. The command is what identifies a Beacon hook, which is
// what every other installer already does and what survives that rewrite.
const kimiManagedComment = "# Beacon endpoint telemetry. Managed by `beacon endpoint hooks install --harness kimi`."

// Kimi Code hook timeouts are seconds, and the runtime's own default is 30.
//
// Stated at the value for the reason the Kiro, Muse, Qwen, OpenHands and DeepSeek constants give:
// the same `timeout` field means milliseconds on some runtimes and seconds here, the files look
// alike, and getting it wrong kills every hook mid-write while the install still reports success.
// Explicit values rather than the inherited default because these are ceilings on a hang, not
// budgets -- a telemetry hook finishes in milliseconds.
//
// Kimi Code validates the field as an integer in 1..600 and refuses to load the whole config file
// if it is outside that range, which is the other reason none of these is left implicit.
//
// The two long ones are long for a reason: the closing events flush cloud telemetry, which has a
// ten-second timeout of its own, and prompt submission can wait on the policy seam.
const (
	kimiSessionStartTimeoutSeconds = 10
	kimiPromptSubmitTimeoutSeconds = 30
	kimiToolTimeoutSeconds         = 10
	kimiApprovalTimeoutSeconds     = 10
	kimiStopTimeoutSeconds         = 45
	kimiSessionEndTimeoutSeconds   = 45
	kimiSubagentTimeoutSeconds     = 10
	kimiCompactionTimeoutSeconds   = 10
)

// kimiEvent binds one Kimi Code lifecycle event to the beacon-hooks subcommand that maps it.
type kimiEvent struct {
	name       string
	subcommand string
	timeout    int
}

// kimiEvents are the events Beacon subscribes to, out of the twenty Kimi Code exposes.
//
// Thirteen of twenty, and what is left out is a decision rather than an oversight:
//
//   - `SessionHeartbeat` fires every sixty seconds for the lifetime of a session, and its timer
//     runs *only when a hook is configured for it* -- so subscribing would not observe an existing
//     behavior, it would create one: a process spawned every minute in every session, writing an
//     event that says nothing happened. That is the one event where the cost of watching exceeds
//     what is watched.
//   - `UserPromptQueued` fires when a message is typed while a turn is still running. The prompt
//     is recorded when it is submitted, which is what UserPromptSubmit is; recording the queueing
//     as well would count one prompt twice.
//   - `TurnStarted` carries a turn id the endpoint schema has no field for, and its `prompt` is
//     the same text UserPromptSubmit already carried.
//   - `TaskStarted` announces a background task, and the tool that started it -- a `Bash` with
//     run_in_background, an `Agent`, an `AskUserQuestion` -- has already been recorded as itself.
//   - `Notification` reports a background task changing state, which is the same activity again.
//   - `StopFailure` reports a turn that failed on an API or runtime error rather than anything the
//     agent did.
//   - `Interrupt` fires in place of Stop when the operator cancels a turn. Beacon's closing action
//     is `tool.completed`, which states that the agent finished; a cancelled turn did not, and
//     there is no action in the endpoint schema that says so. Recording it as a completion would
//     be worse than not recording it, and the session is still closed by SessionEnd.
//
// PostToolUseFailure shares a subcommand with PostToolUse, and PermissionResult shares one with
// PermissionRequest. Both pairs are one thing observed twice, and the mapper tells them apart from
// `hook_event_name`; splitting either across two subcommands would mean two readers that have to
// agree about what a failure or a decision is.
var kimiEvents = []kimiEvent{
	{"SessionStart", "session-start", kimiSessionStartTimeoutSeconds},
	{"UserPromptSubmit", "prompt-submit", kimiPromptSubmitTimeoutSeconds},
	{"PreToolUse", "pre-tool", kimiToolTimeoutSeconds},
	{"PostToolUse", "post-tool", kimiToolTimeoutSeconds},
	{"PostToolUseFailure", "post-tool", kimiToolTimeoutSeconds},
	{"PermissionRequest", "permission-request", kimiApprovalTimeoutSeconds},
	{"PermissionResult", "permission-request", kimiApprovalTimeoutSeconds},
	{"Stop", "stop", kimiStopTimeoutSeconds},
	{"SessionEnd", "session-end", kimiSessionEndTimeoutSeconds},
	{"SubagentStart", "subagent-start", kimiSubagentTimeoutSeconds},
	{"SubagentStop", "subagent-stop", kimiSubagentTimeoutSeconds},
	{"PreCompact", "pre-compact", kimiCompactionTimeoutSeconds},
	{"PostCompact", "post-compact", kimiCompactionTimeoutSeconds},
}

type KimiOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type KimiStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	HooksPath  string `json:"hooks_path,omitempty"`
	Message    string `json:"message,omitempty"`
}

// kimiHookEntry is one row of the `[[hooks]]` array, as Kimi Code defines it.
//
// Exactly four fields, because the runtime's schema is strict: an unknown key in a hook entry
// fails validation and the whole config file stops loading. So this struct is not a convenience
// subset -- it is the complete shape, and adding a field to it would break every Kimi Code
// session on the machine.
type kimiHookEntry struct {
	Event   string `toml:"event"`
	Matcher string `toml:"matcher,omitempty"`
	Command string `toml:"command"`
	Timeout int    `toml:"timeout,omitempty"`
}

var kimiRuntime = hookRuntime{
	displayName: "Kimi Code",
	configPath:  kimiConfigPath,
	install:     installKimiHooks,
	uninstall:   removeKimiHooks,
	isInstalled: isKimiInstalledAt,
}

func InstallKimi(opts KimiOptions) (KimiStatus, error) {
	status, err := installRuntimeHooks(kimiRuntime, RuntimeOptions(opts))
	if err != nil {
		return KimiStatus{}, err
	}
	return kimiStatusFromRuntime(status), nil
}

func UninstallKimi(opts KimiOptions) (KimiStatus, error) {
	status, err := uninstallRuntimeHooks(kimiRuntime, RuntimeOptions(opts))
	if err != nil {
		return KimiStatus{}, err
	}
	return kimiStatusFromRuntime(status), nil
}

func KimiHookStatus(opts KimiOptions) KimiStatus {
	return kimiStatusFromRuntime(runtimeHookStatus(kimiRuntime, RuntimeOptions(opts)))
}

func IsKimiInstalled(opts KimiOptions) bool {
	return isRuntimeInstalled(kimiRuntime, RuntimeOptions(opts))
}

func kimiStatusFromRuntime(status runtimeStatus) KimiStatus {
	return KimiStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		HooksPath:  status.ConfigPath,
		Message:    status.Message,
	}
}

// kimiCommandPrefix builds the command Kimi Code will execute, quoted for the shell that will
// parse it on this machine.
//
// This is the per-runtime branch hookCommandQuote's own comment anticipates. Kimi Code runs a hook
// through Node's `spawn(command, [], {shell: true})`, which is `/bin/sh -c` on POSIX and
// `%ComSpec% /d /s /c` -- cmd.exe -- on Windows. Single quotes are not quoting in cmd.exe: they
// are ordinary characters, so a quoted path under `C:\Program Files\` would be looked up verbatim
// with the quotes in it, the spawn would fail, and Kimi Code is fail-open on a hook that cannot
// start. The install would report success and collect nothing, with no error anywhere.
//
// Double quotes are what cmd.exe reads, and a Windows path cannot contain one -- `"` is an illegal
// filename character -- so there is nothing to escape. The one case this does not cover is stated
// rather than hidden: cmd.exe still expands `%VAR%` inside double quotes, so a Beacon install
// under a directory whose name contains a percent sign would have that fragment substituted away.
// There is no command-line escape for it, and the alternative -- leaving the path unquoted --
// fails on the far more common space.
//
// The detection side needs no matching change: commandFields already accepts both quote
// characters, precisely so a command written for either shell can be recognized.
func kimiCommandPrefix(binaryPath, logPath, configPath string) string {
	if runtime.GOOS != "windows" {
		return endpointCommandPrefix("kimi", binaryPath, logPath, configPath)
	}
	args := []string{kimiWindowsQuote(binaryPath), "--platform", "kimi"}
	if logPath != "" {
		args = append(args, "--log", kimiWindowsQuote(logPath))
	}
	if configPath != "" {
		args = append(args, "--config", kimiWindowsQuote(configPath))
	}
	return strings.Join(args, " ")
}

func kimiWindowsQuote(value string) string {
	return `"` + value + `"`
}

// kimiBlock renders the `[[hooks]]` entries Beacon appends, as TOML text.
//
// Rendered rather than marshalled, and the reason is the comment: go-toml writes a document, and
// a document has no place for a line of prose above an array element. The values that could need
// escaping are written through the marshaller anyway -- see kimiTOMLString -- so what is
// hand-written here is only the structure.
func kimiBlock(binaryPath, logPath, configPath, eol string) (string, error) {
	prefix := kimiCommandPrefix(binaryPath, logPath, configPath)

	var builder strings.Builder
	builder.WriteString(kimiManagedComment)
	builder.WriteString(eol)
	for index, event := range kimiEvents {
		command, err := kimiTOMLString(prefix + " " + event.subcommand)
		if err != nil {
			return "", err
		}
		eventName, err := kimiTOMLString(event.name)
		if err != nil {
			return "", err
		}
		builder.WriteString("[[" + kimiHooksKey + "]]" + eol)
		builder.WriteString("event = " + eventName + eol)
		// `matcher` is omitted rather than written as ".*" or "*". Kimi Code treats an absent or
		// empty matcher as match-all, and compiles a present one as a regular expression -- where
		// a pattern it cannot compile matches nothing at all, silently. An absent field has no
		// such failure mode, and match-all is what a telemetry hook wants on every event.
		builder.WriteString("command = " + command + eol)
		builder.WriteString(fmt.Sprintf("timeout = %d"+eol, event.timeout))
		if index < len(kimiEvents)-1 {
			builder.WriteString(eol)
		}
	}
	return builder.String(), nil
}

// kimiTOMLString renders one string as a TOML value, through the marshaller rather than by hand.
//
// The values are a path and an event name, so quoting them by hand would work today. It is done
// this way because the one input that is not Beacon's own -- the install directory, which reaches
// here inside the command -- can contain a backslash on every Windows machine and a quote
// character on a POSIX one, and a hand-rolled quoter that got either wrong would write a config
// file the runtime refuses to load.
func kimiTOMLString(value string) (string, error) {
	encoded, err := toml.Marshal(map[string]string{"v": value})
	if err != nil {
		return "", err
	}
	_, rendered, found := strings.Cut(strings.TrimRight(string(encoded), "\r\n"), "= ")
	if !found {
		return "", fmt.Errorf("could not render %q as a TOML string", value)
	}
	return rendered, nil
}

// installKimiHooks appends Beacon's block to the user's config, replacing any block it left
// behind before.
func installKimiHooks(path, binaryPath, logPath, configPath string) error {
	original, err := readKimiConfigText(path)
	if err != nil {
		return err
	}
	parsed, err := parseKimiConfig(path, original)
	if err != nil {
		return err
	}
	if err := kimiHooksAreAppendable(path, original, parsed); err != nil {
		return err
	}

	eol := kimiLineEnding(original)
	stripped, kept := stripKimiHooks(original)
	block, err := kimiBlock(binaryPath, logPath, configPath, eol)
	if err != nil {
		return err
	}
	updated := kimiEndWithNewline(kimiJoin(stripped, block, eol), eol)

	want := append(kept, kimiExpectedEntries(binaryPath, logPath, configPath)...)
	if err := verifyKimiRewrite(path, original, updated, want); err != nil {
		return err
	}
	return writeKimiConfig(path, updated)
}

// removeKimiHooks takes Beacon's entries back out, and reports whether any were there.
//
// A config that Beacon never wrote into, or that has since been made unparseable by something
// else, is left alone rather than rewritten: an uninstall may remove what it added and nothing
// more, and this file is the user's.
func removeKimiHooks(path string) (bool, error) {
	original, err := readKimiConfigText(path)
	if err != nil {
		return false, err
	}
	if original == "" {
		return false, nil
	}
	parsed, err := parseKimiConfig(path, original)
	if err != nil {
		// A config Kimi Code cannot load is one it is already refusing to start with. Rewriting
		// it here would replace a problem the user can see with one Beacon caused, and the hooks
		// in it are not running either way.
		return false, nil
	}
	if !kimiConfigHasEndpointHooks(parsed) {
		return false, nil
	}

	stripped, kept := stripKimiHooks(original)
	updated := kimiEndWithNewline(stripped, kimiLineEnding(original))
	if err := verifyKimiRewrite(path, original, updated, kept); err != nil {
		return false, err
	}
	return true, writeKimiConfig(path, updated)
}

func isKimiInstalledAt(path string) bool {
	original, err := readKimiConfigText(path)
	if err != nil || original == "" {
		return false
	}
	parsed, err := parseKimiConfig(path, original)
	if err != nil {
		return false
	}
	return kimiConfigHasEndpointHooks(parsed)
}

// kimiConfigHasEndpointHooks reports whether the parsed config registers a Beacon command.
//
// By the command, not by the presence of a `hooks` array and not by Beacon's comment, for the
// reason every other installer gives and one more that is specific to this file: Kimi Code's
// legacy migration rewrites config.toml by serializing it, which keeps the entries and drops the
// comment.
func kimiConfigHasEndpointHooks(parsed map[string]interface{}) bool {
	for _, entry := range kimiHookEntries(parsed) {
		if isEndpointHookCommand(entry.Command, "kimi") {
			return true
		}
	}
	return false
}

// kimiHookEntries reads the `hooks` array out of a parsed config.
//
// Tolerant of a malformed row rather than failing on one: a hook entry that is not a table, or
// whose `command` is not a string, is somebody else's problem -- Kimi Code will reject the file
// on its own -- and refusing to read the rest of the array would mean Beacon could not find its
// own hooks in a file that has one bad row.
func kimiHookEntries(parsed map[string]interface{}) []kimiHookEntry {
	raw, ok := parsed[kimiHooksKey].([]interface{})
	if !ok {
		return nil
	}
	entries := make([]kimiHookEntry, 0, len(raw))
	for _, item := range raw {
		table, ok := item.(map[string]interface{})
		if !ok {
			entries = append(entries, kimiHookEntry{})
			continue
		}
		entry := kimiHookEntry{}
		entry.Event, _ = table["event"].(string)
		entry.Matcher, _ = table["matcher"].(string)
		entry.Command, _ = table["command"].(string)
		if timeout, ok := table["timeout"].(int64); ok {
			entry.Timeout = int(timeout)
		}
		entries = append(entries, entry)
	}
	return entries
}

// kimiHooksAreAppendable reports whether Beacon may append `[[hooks]]` to this document.
//
// It may not when `hooks` was written as an inline array -- `hooks = [{event = "...", ...}]`.
// TOML treats that key as already defined, so an appended `[[hooks]]` header is a redefinition
// and the whole document stops parsing. On this runtime that is not a degraded install: Kimi Code
// refuses to start on a config it cannot load, so Beacon would have taken the user's agent down.
//
// Detected by asking the text rather than the parse, because the parse cannot tell the two
// spellings apart -- both produce an array of tables. A document that has `hooks` but no
// `[[hooks]]` header wrote it inline.
func kimiHooksAreAppendable(path, text string, parsed map[string]interface{}) error {
	if _, present := parsed[kimiHooksKey]; !present {
		return nil
	}
	if kimiCountHookHeaders(text) > 0 {
		return nil
	}
	return fmt.Errorf(
		"%s defines `hooks` as an inline array, and TOML does not allow [[hooks]] entries to be "+
			"added after one -- appending would leave a config file Kimi Code refuses to start "+
			"with. Rewrite the existing hooks as [[hooks]] blocks and run the install again",
		path)
}

// kimiHookHeaderPattern matches an array-of-tables header for the hooks key.
//
// Whitespace inside the brackets is allowed because TOML allows it, and a trailing comment is
// allowed because a person may have written one. A dotted or quoted spelling is deliberately not
// matched: `["hooks"]` and `[a.hooks]` are different keys, and treating them as this one would
// have Beacon delete a table it does not own.
func kimiHookHeaderPattern(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[[") {
		return false
	}
	closing := strings.Index(trimmed, "]]")
	if closing < 0 {
		return false
	}
	if strings.TrimSpace(trimmed[2:closing]) != kimiHooksKey {
		return false
	}
	rest := strings.TrimSpace(trimmed[closing+2:])
	return rest == "" || strings.HasPrefix(rest, "#")
}

// kimiIsTopLevelHeader reports whether a line opens any table, which is what ends a hooks block.
func kimiIsTopLevelHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

func kimiCountHookHeaders(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if kimiHookHeaderPattern(line) {
			count++
		}
	}
	return count
}

// stripKimiHooks removes every `[[hooks]]` block whose command is Beacon's, and returns the
// remaining text alongside the hook entries that survived.
//
// Line-based, and deliberately unsophisticated about TOML's harder corners: it does not track
// multi-line strings, so a `"""` value elsewhere in the document containing a line that looks like
// a `[[hooks]]` header would confuse it. That is safe rather than merely unlikely, because nothing
// this function produces is written without first being parsed and compared -- see
// verifyKimiRewrite. A scanning mistake fails the operation with an explanation; it cannot damage
// the file.
//
// Beacon's own comment line is removed with the block when it sits directly above one, which
// keeps a reinstall from stacking comments. It is not required to be there: the block is
// identified by its command.
func stripKimiHooks(text string) (string, []kimiHookEntry) {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	keptEntries := []kimiHookEntry{}

	for index := 0; index < len(lines); index++ {
		if !kimiHookHeaderPattern(lines[index]) {
			kept = append(kept, lines[index])
			continue
		}
		end := index + 1
		for end < len(lines) && !kimiIsTopLevelHeader(lines[end]) {
			end++
		}
		block := lines[index:end]
		entry := kimiEntryFromBlock(block)
		if !isEndpointHookCommand(entry.Command, "kimi") {
			kept = append(kept, block...)
			keptEntries = append(keptEntries, entry)
			index = end - 1
			continue
		}
		// Drop the block, and with it the label line and the blank line Beacon writes above it,
		// so an install that runs twice does not leave a trail of orphaned comments.
		for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == kimiManagedComment {
			kept = kept[:len(kept)-1]
		}
		for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
			kept = kept[:len(kept)-1]
		}
		index = end - 1
	}
	return strings.Join(kept, "\n"), keptEntries
}

// kimiEntryFromBlock reads one hook entry out of the lines of its block.
//
// Only `command` is read as a value, because it is the only field this file decides anything on.
// The rest of the entry is filled from the same lines so the verification below compares like
// with like: the parser will report every field, so the scanner has to as well.
func kimiEntryFromBlock(block []string) kimiHookEntry {
	entry := kimiHookEntry{}
	for _, line := range block[1:] {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "event":
			entry.Event = kimiUnquote(value)
		case "matcher":
			entry.Matcher = kimiUnquote(value)
		case "command":
			entry.Command = kimiUnquote(value)
		case "timeout":
			entry.Timeout = kimiAtoi(strings.TrimSpace(value))
		}
	}
	return entry
}

// kimiUnquote recovers a TOML string value's contents well enough to compare it.
//
// Through the parser, so the escape rules are TOML's rather than an approximation of them. A value
// the parser cannot read comes back as the raw text, which fails the comparison in
// verifyKimiRewrite rather than passing it by accident.
func kimiUnquote(value string) string {
	var holder struct {
		V string `toml:"v"`
	}
	if err := toml.Unmarshal([]byte("v ="+value+"\n"), &holder); err != nil {
		return strings.TrimSpace(value)
	}
	return holder.V
}

func kimiAtoi(value string) int {
	var holder struct {
		V int `toml:"v"`
	}
	if err := toml.Unmarshal([]byte("v = "+value+"\n"), &holder); err != nil {
		return 0
	}
	return holder.V
}

// kimiExpectedEntries is what Beacon's block should parse back into.
func kimiExpectedEntries(binaryPath, logPath, configPath string) []kimiHookEntry {
	prefix := kimiCommandPrefix(binaryPath, logPath, configPath)
	entries := make([]kimiHookEntry, 0, len(kimiEvents))
	for _, event := range kimiEvents {
		entries = append(entries, kimiHookEntry{
			Event:   event.name,
			Command: prefix + " " + event.subcommand,
			Timeout: event.timeout,
		})
	}
	return entries
}

// verifyKimiRewrite refuses a write that would change anything other than the hooks array.
//
// This is the safety net that makes a line-based edit of somebody's credentials file defensible.
// The proposed text is parsed and compared against the original parse twice over: every top-level
// key other than `hooks` must be deeply equal, and `hooks` must be exactly the entries the caller
// intended. So a scanner that misread a multi-line string, a quoting mistake in a rendered
// command, or a block boundary off by one fails here with an explanation, before anything reaches
// the disk.
//
// The same idea as the verification Kimi Code's own config writer performs before it replaces this
// file, and for the same reason: a surgical text edit is worth doing only if it can be checked.
func verifyKimiRewrite(path, original, updated string, want []kimiHookEntry) error {
	before, err := parseKimiConfig(path, original)
	if err != nil {
		return err
	}
	after, err := parseKimiConfig(path, updated)
	if err != nil {
		return fmt.Errorf("Beacon's edit to %s would not parse as TOML (%w); the file was left "+
			"unchanged", path, err)
	}

	for key, value := range before {
		if key == kimiHooksKey {
			continue
		}
		other, present := after[key]
		if !present || !reflect.DeepEqual(value, other) {
			return fmt.Errorf("Beacon's edit to %s would have changed %q, which it does not own; "+
				"the file was left unchanged", path, key)
		}
	}
	for key := range after {
		if key == kimiHooksKey {
			continue
		}
		if _, present := before[key]; !present {
			return fmt.Errorf("Beacon's edit to %s would have added %q, which it does not own; "+
				"the file was left unchanged", path, key)
		}
	}

	got := kimiHookEntries(after)
	if len(want) == 0 && len(got) == 0 {
		return nil
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("Beacon's edit to %s would have left %d hook entries rather than %d; "+
			"the file was left unchanged", path, len(got), len(want))
	}
	return nil
}

// kimiJoin appends a block to a document, keeping exactly one blank line between them.
func kimiJoin(text, block, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return block
	}
	return trimmed + eol + eol + block
}

// kimiEndWithNewline gives a non-empty document exactly one trailing line ending.
//
// stripKimiHooks trims the blank lines that sat above a removed block, which on a config whose
// only hooks were Beacon's takes the file's final newline with them. A POSIX text file ends in a
// newline, several editors add one back on the next save, and a config that gained or lost one
// every time Beacon was installed and removed would show up as a spurious diff in any dotfiles
// repository the user keeps.
func kimiEndWithNewline(text, eol string) string {
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + eol
}

// kimiLineEnding is the line ending the existing document uses, so an edit does not mix them.
//
// A file with no newline at all -- empty, or one line -- gets the platform's, which is what a
// fresh install on Windows should write.
func kimiLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\n") {
		return "\n"
	}
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

func readKimiConfigText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func parseKimiConfig(path, text string) (map[string]interface{}, error) {
	parsed := map[string]interface{}{}
	if strings.TrimSpace(text) == "" {
		return parsed, nil
	}
	if err := toml.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf(
			"%s is not valid TOML (%w). Kimi Code refuses to start on a config it cannot load, so "+
				"Beacon will not write into one -- fix the file and run the install again",
			path, err)
	}
	return parsed, nil
}

// writeKimiConfig replaces the config, keeping whatever mode it already had.
//
// 0600 for a file Beacon creates, which is stricter than the 0644 the other installers use and is
// not a matter of taste: this file is where Kimi Code keeps `api_key` in plain text, so a config
// Beacon brings into existence must not be world-readable even though it has nothing secret in it
// yet. An existing file keeps its own mode -- the user's choice about their own file, and the
// runtime may be launched by a different account.
func writeKimiConfig(path, text string) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), mode)
}

// KimiConfigPath is where Beacon registers Kimi Code's hooks.
//
// Exported because `harness` reports on the same file and must not rebuild the path itself. Two
// copies of "where the config lives" is how discovery comes to report on a file the installer does
// not write -- and here that would be worse than usual, because the value it reports on is the one
// holding the user's API keys.
func KimiConfigPath(level Level) (string, error) {
	return kimiConfigPath(level)
}

func kimiConfigPath(level Level) (string, error) {
	dir, err := kimiHomeDir(level)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, kimiConfigFileName), nil
}

// kimiHomeDir resolves the Kimi Code data root for a scope.
//
// User scope only, and the project case is an error carrying its reason rather than a silent
// fallback to user scope. Falling back would be worse than refusing: an operator asking for a
// repository-scoped install would get a machine-wide one with no indication that is what
// happened.
//
// The refusal is a property of the runtime rather than a limitation here. Kimi Code reads one
// user-level config file and has no project-level config mechanism at all; the project-local
// `.kimi-code/` directory holds only a workspace override and an MCP server list, neither of
// which can register a hook. Its own answer to per-project configuration is to point
// KIMI_CODE_HOME somewhere else, which this function already honors.
func kimiHomeDir(level Level) (string, error) {
	switch level {
	case "", LevelUser:
		if base := strings.TrimSpace(os.Getenv(kimiEnvHome)); base != "" {
			return base, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, kimiHomeDirName), nil
	case LevelProject:
		return "", fmt.Errorf(
			"Kimi Code has no project-scoped hook configuration: it reads one user-level " +
				"config.toml, and the project-local .kimi-code directory holds only a workspace " +
				"override and an MCP server list. Install at user scope, which covers every " +
				"project on this machine, or point KIMI_CODE_HOME at a per-project data root and " +
				"install again under it")
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
