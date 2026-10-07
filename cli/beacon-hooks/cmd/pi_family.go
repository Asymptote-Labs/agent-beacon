package cmd

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// The Pi family is Pi (pi.dev) and the forks that kept its extension API.
//
// Oh My Pi and Prime Agent are both hard forks of pi-mono, and their extension event payloads are
// structurally the same as Pi's: the same `type` discriminator, the same `toolName`/`input`/
// `details` shape on tool events, the same assistant message parts, and the same
// input/output/cacheRead/cacheWrite/cost usage object. Mapping them three times would mean three
// copies of that knowledge drifting apart -- and the drift would be silent, because a mapper that
// stops recognizing a field emits an event missing a column rather than an error.
//
// So the shape lives here once, and each runtime supplies only what actually differs: which
// `--platform` it was installed as, and what to call it in an event message. What must NOT be
// shared is the identity: they are separately installed products, they get separate harness names
// (see asymptoteobserve.NormalizeHarnessName), and every event carries its own runtime's name in
// `raw` so an operator reading a row can tell which binary produced it.
//
// Events one runtime has and the others do not are mapped here too, and gated by the subscription
// list and the tool catalog rather than by a branch: Oh My Pi's extension subscribes to its
// approval and user_python events and Pi's does not, and only Prime Agent ships an `ipython` tool,
// so those cases are simply never reached for the runtimes that lack them. That is a stronger
// guarantee than a platform check would be -- Beacon cannot synthesize a Pi approval because Pi
// never sends one, not because a condition remembered to say so. Inventing one to keep the
// runtimes symmetric would put a decision nobody made into the log.
type piFamily struct {
	// platform is the `--platform` value the runtime's hook is installed with. It selects the
	// session-id and working-directory readers in helpers.go, keys this runtime's block inside
	// `raw`, and prefixes the runtime-specific keys nested under it.
	platform string
	// displayName is how the runtime is named in an event's human-readable message.
	displayName string
	// resolvesToolPaths marks a runtime whose read, edit and write tools take a path the way Oh
	// My Pi's do -- relative to the session's working directory or to `~`, with an inline selector
	// such as `main.go:10-40` or `notes.md:raw` -- and whose results report the absolute file they
	// resolved it to. Such a runtime's file events carry that file; see filePath.
	//
	// It is declared rather than inferred because Pi and Prime Agent are also read back from their
	// session files (pisession, primesession), which record the path as written. A hook event and
	// a session-file event for the same read are merged only when their file.path matches, so
	// resolving the path on one side alone would record each read twice.
	resolvesToolPaths bool
}

var (
	piRuntime  = piFamily{platform: "pi", displayName: "Pi"}
	ompRuntime = piFamily{platform: "omp", displayName: "Oh My Pi", resolvesToolPaths: true}
	// Prime Agent's `--platform` is `prime` rather than `prime-agent` because the platform value is
	// also the prefix on this runtime's keys inside `raw`, and `prime_agent_session_reason` reads
	// as a field of a harness named `prime_agent` -- which is exactly what it is. The harness name
	// events are written under is still `prime_agent`; NormalizeHarnessName pins both spellings so
	// one session cannot be recorded under two names.
	primeRuntime = piFamily{platform: "prime", displayName: "Prime Agent"}
	// Senpi is the standalone edition of oh-my-openagent: an in-flight fork of pi-mono
	// (code-yeongyu/senpi) that OMO brands and bundles its own extension into, distributed as the
	// `omo` command. Its ExtensionEvent union kept Pi's shape -- the same `type` discriminator, the
	// same toolName/input/details tool events, the same usage object -- even where it added events
	// Pi does not have, so the seven events Beacon's Senpi extension subscribes to map through this
	// family unchanged. "omo" rather than "senpi" because that is the binary the operator runs and
	// the directory Beacon installs into (~/.omo/agent); "senpi" is the upstream project name, not
	// the product. The harness name events are written under is omo_senpi, not omo, because "omo"
	// alone would not distinguish this edition from oh-my-openagent's OpenCode and Codex CLI
	// editions if Beacon ever observes those too -- see asymptoteobserve.NormalizeHarnessName.
	omoRuntime = piFamily{platform: "omo", displayName: "Senpi"}
)

// rawKey namespaces a runtime-specific detail inside the `raw` block.
//
// These keys sit beside the verbatim payload rather than being promoted to schema fields, because
// they describe how one runtime happened to phrase something rather than a fact the endpoint event
// schema defines. Prefixing them with the platform keeps a Pi row and an Oh My Pi row from
// colliding in a store that flattens `raw`.
func (f piFamily) rawKey(suffix string) string {
	return f.platform + "_" + suffix
}

// endpointEvents maps one Pi-family payload onto the endpoint events it justifies.
//
// An unrecognized type returns nothing rather than a generic event, for the same reason the Cline
// mapper drops unknown stages: these runtimes publish far more events than the extension
// subscribes to, and a future one arriving here should be silent rather than becoming an
// undifferentiated "something happened" row that every query matches and none can explain.
//
// Both halves of a tool call carry the runtime's own `toolCallId`, and both promote it to
// `gen_ai.tool.call.id` through the shared alias list in asymptoteobserve.ToolCallIDKeys. That
// field is the only thing linking tool.invoked to the tool.completed, file.modified or
// command.executed it turned into -- and, on Oh My Pi, an approval decision to the execution it
// approved. Without it those rows sit in the log as unrelated events that merely happen to share a
// session id and a nearby timestamp.
func (f piFamily) endpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	fields := f.baseFields(input, sessionID)

	switch getFirstStr(input, "type") {
	case "session_start":
		// The runtime reports why the session started -- startup, reload, new, resume, fork -- and
		// the distinction matters for reading a log: a fork and a resume both produce a session id
		// that has history behind it, which a reader counting sessions needs to know.
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("session_reason"): reason})
		}
		return f.one("session.started", "session", "info", "session started", fields)

	case "session_shutdown":
		if reason := getFirstStr(input, "reason"); reason != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("shutdown_reason"): reason})
		}
		return f.one("session.ended", "session", "info", "session ended", fields)

	case "input":
		prompt := getFirstStr(input, "text")
		if prompt == "" {
			return nil
		}
		events := []normalizedEvent{f.promptEvent(fields, prompt, getFirstStr(input, "source"))}
		if link, ok := handoffLinkEvent(fields, prompt); ok {
			events = append(events, link)
		}
		return events

	case "context":
		// The skill index the model was shown, which the extension reads from the system prompt
		// and sends in place of the event itself. It is system context, not a prompt: the operator
		// did not write it, so it is never recorded under `prompt` or as input.
		listing := asymptoteobserve.ParseSkillListing(asymptoteobserve.SystemContextSourceSystemPrompt, getFirstStr(input, "skillListing"), nil)
		skill := listing.Fields(asymptoteobserve.DefaultStringLimit)
		if skill == nil {
			return nil
		}
		fields["gen_ai"] = mergeNested(fields["gen_ai"], skill["gen_ai"].(map[string]interface{}))
		fields["system_context"] = skill["system_context"]
		fields["content"] = skill["content"]
		// baseFields copied the whole input under raw, which would store the listing a second time.
		fields["raw"] = map[string]interface{}{f.platform: map[string]interface{}{"type": "context"}}
		return f.one("session.context", "session", "info", "system skill listing exposed to the model", fields)

	case "tool_call":
		// The pre-execution half of a tool call: the runtime has decided to run it and named its
		// arguments, but nothing has happened yet. Recorded as tool.invoked to match the Cline
		// mapper's tool_before stage, and deliberately not as an approval -- a tool_call handler
		// can block, but that is an extension deciding rather than an operator being asked. Oh My
		// Pi's real approval decisions arrive as their own events and are mapped there.
		call := piToolCallOf(input)
		mergeMap(fields, f.toolFields(call, input, false))
		f.applyPythonMarker(fields, call.name)
		applyToolCallID(fields, input)
		return f.one("tool.invoked", "tool", "info", "tool invoked", fields)

	case "tool_result":
		return f.toolResultEvents(input, fields)

	case "user_bash":
		// A command the human ran with the `!` prefix rather than one the agent chose. No tool
		// event covers it, and it is the one command shape here that the agent did not originate,
		// so it is recorded with the operator noted in raw rather than silently merged in with
		// agent-run commands.
		command := getFirstStr(input, "command")
		if command == "" {
			return nil
		}
		fields["command"] = map[string]interface{}{"command": command}
		fields["tool"] = map[string]interface{}{"name": "user_bash", "command": command}
		fields["content"] = retainedContentFields(command)
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			f.rawKey("user_initiated"):       true,
			f.rawKey("exclude_from_context"): input["excludeFromContext"],
		})
		return f.one("command.executed", "command", "info", "user command executed", fields)

	case "user_python":
		// The `$` prefix runs Python in the runtime's own REPL rather than a shell. It is the
		// operator's code, not the agent's, and it is executed just as literally as a bash command
		// -- `os.system("rm -rf /")` is a shell command wearing a Python hat -- so it is recorded
		// in the command category where the risky-command rules can see it, marked as the
		// operator's and as Python rather than being passed off as a shell command.
		code := getFirstStr(input, "code")
		if code == "" {
			return nil
		}
		fields["command"] = map[string]interface{}{"command": code}
		fields["tool"] = map[string]interface{}{"name": "user_python", "command": code}
		fields["content"] = retainedContentFields(code)
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{
			f.rawKey("user_initiated"):       true,
			f.rawKey("user_python"):          true,
			f.rawKey("exclude_from_context"): input["excludeFromContext"],
		})
		return f.one("command.executed", "command", "info", "user Python executed", fields)

	case "tool_approval_requested":
		return f.approvalEvents(input, fields, "approval.requested", "requested", "approval requested")

	case "tool_approval_resolved":
		// The runtime states the outcome as a boolean rather than as a word, so there is no
		// unknown case to fall back to: an event that reached here had a decision made on it.
		if approved, _ := input["approved"].(bool); approved {
			return f.approvalEvents(input, fields, "approval.allowed", "approve", "approval allowed")
		}
		return f.approvalEvents(input, fields, "approval.denied", "deny", "approval denied")

	case "message_end":
		return f.messageEndEvents(input, fields)

	default:
		return nil
	}
}

// approvalEvents records an operator approval decision the runtime actually asked for.
//
// This is the one thing Oh My Pi exposes that Pi does not. It matters because Beacon has always
// refused to synthesize an approval from a tool call: a `tool_call` handler that blocks is an
// extension deciding, and recording that as an approval would be indistinguishable from a decision
// a human made. Here the runtime reports a real prompt and a real answer, so the event is
// `observed` rather than `inferred` and carries the operator's decision verbatim.
//
// The runtime does not put the tool's arguments on these events, so the extension carries them: it
// remembers the `tool_call` that proposed the call and attaches its `input` to the approval under
// the same key. That is what makes the approval readable by a detection. Every approval rule Beacon
// ships matches on `command.command` or `file.path` rather than on a tool name, so an approval that
// said only "the operator denied bash" would be telemetry no rule could act on.
//
// When the arguments are absent -- an approval for a call the extension never saw proposed -- the
// event still records the decision, the tool name and the call id. The call id is the join back to
// the tool.invoked that does carry the arguments, which is why it is promoted here as carefully as
// on the tool events themselves.
func (f piFamily) approvalEvents(input, fields map[string]interface{}, action, decision, messageSuffix string) []normalizedEvent {
	if piToolName(input) != "" {
		// toolFields resolves the command, file and MCP blocks from the decided call's arguments
		// when the extension attached them, and yields just the tool name when it did not.
		mergeMap(fields, f.toolFields(piToolCallOf(input), input, false))
	}

	approval := map[string]interface{}{"required": true, "decision": decision}
	// The runtime's own words for why, when it gave any. Left absent rather than filled with a
	// Beacon-authored sentence, so a reader can tell an operator's reason from a default.
	if reason := getFirstStr(input, "reason"); reason != "" {
		approval["reason"] = reason
	}
	fields["approval"] = approval

	// Which approval policy the session was running under. "yolo" means the operator turned the
	// prompts off, which is the single most load-bearing fact about any approval row: it is the
	// difference between a decision someone made and a decision nobody was asked to make.
	if mode := getFirstStr(input, "approvalMode"); mode != "" {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("approval_mode"): mode})
	}

	applyToolCallID(fields, input)
	return f.one(action, "approval", "info", messageSuffix, fields)
}

// one wraps a single event, prefixing the runtime's display name onto the message.
//
// Messages are built from a suffix rather than spelled out per runtime so that "Pi tool failed"
// and "Oh My Pi tool failed" cannot drift into describing the same thing two different ways.
func (f piFamily) one(action, category, severity, messageSuffix string, values map[string]interface{}) []normalizedEvent {
	return []normalizedEvent{{
		action:   action,
		category: category,
		severity: severity,
		message:  f.displayName + " " + messageSuffix,
		fields:   values,
	}}
}

func (f piFamily) baseFields(input map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, f.platform)
	applyWorkspaceFieldsForPlatform(fields, input, "", f.platform)
	fields["raw"] = map[string]interface{}{f.platform: input}
	if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}
	return fields
}

func (f piFamily) promptEvent(fields map[string]interface{}, prompt, source string) normalizedEvent {
	fields["prompt"] = map[string]interface{}{"text": prompt}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
	})
	fields["content"] = retainedContentFields(prompt)
	if source != "" {
		// These runtimes distinguish interactive input from input delivered over their RPC surface
		// or injected by another extension. Retained because "a human typed this" and "a script
		// sent this" are different facts about the same prompt.
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("input_source"): source})
	}
	return normalizedEvent{
		action: "prompt.submitted", category: "prompt", severity: "info",
		message: "Prompt submitted to " + f.displayName, fields: fields,
	}
}

// piToolName reads the tool name off a tool_call or tool_result payload.
func piToolName(input map[string]interface{}) string {
	return getFirstStr(input, "toolName", "tool_name")
}

// piToolInput returns a tool call's arguments.
//
// tool_call carries them under `input`; tool_result carries the same arguments under `input` too,
// which is what lets one function serve both and a file path survive onto the result event.
func piToolInput(input map[string]interface{}) map[string]interface{} {
	if args := firstMap(input, "input", "args"); args != nil {
		return args
	}
	return map[string]interface{}{}
}

// piToolCall is the tool call one Pi-family tool event describes: the tool that ran, the arguments
// it ran with, and the details the runtime reported for it.
//
// It is usually just the payload's own toolName, input and details. The exception is a call routed
// through Oh My Pi's tool-device transport, which reaches the tool by writing to an `xd://` URI;
// see ompDeviceCall.
type piToolCall struct {
	name    string
	args    map[string]interface{}
	details map[string]interface{}
}

// piToolCallOf reads the tool call a tool_call, tool_result or approval payload describes.
func piToolCallOf(input map[string]interface{}) piToolCall {
	call := piToolCall{name: piToolName(input), args: piToolInput(input), details: firstMap(input, "details")}
	if device, ok := ompDeviceCall(call); ok {
		return device
	}
	return call
}

// ompDeviceScheme is the URI scheme through which Oh My Pi's read and write tools reach a tool
// device: `read xd://<tool>` returns the tool's documentation, and `write xd://<tool>` with a JSON
// payload calls it.
const ompDeviceScheme = "xd://"

// ompDeviceHelpContent matches a device write that asks for documentation rather than making a
// call: an empty payload, `?`, or `help`. It mirrors HELP_CONTENT_RE in Oh My Pi's tools/xdev.ts,
// which is how the runtime itself tells the two apart before anything has run.
var ompDeviceHelpContent = regexp.MustCompile(`(?i)^\s*(\?|help)?\s*$`)

// ompDeviceCall resolves a write to `xd://<tool>` into the call of <tool> it carries.
//
// Oh My Pi exposes MCP tools and its optional built-in tools as devices rather than as top-level
// tools, so the model calls one by writing its arguments to the device's URI. Nothing is written
// anywhere: the write tool is the transport, and the device is the tool that ran. Recording the
// write as itself would put a file.created for `xd://mcp__…` in the log, and would name the call
// `write` -- a different tool from the one every other runtime records when its agent calls the same
// MCP server. So the call is recorded as the device: its name, the arguments it was given, and the
// details it reported, which the runtime nests under `details.xdev`.
//
// The runtime also reports most device calls a second time from inside the dispatch, as the
// device's own tool_call and tool_result under the same toolCallId. Both reports now describe the
// same tool with the same call id, which is exactly what the endpoint writer's call-id dedupe treats
// as one call -- so the call lands in the log once, as it would on any other runtime. Devices the
// runtime handles in-process (report_issue, and its plan resolve/reject/propose devices) send no
// second report, and are recorded from the write alone; nothing here needs to know which is which.
//
// A help request is a documentation lookup, the same as reading the device's URI, and is left as
// the write it is.
func ompDeviceCall(call piToolCall) (piToolCall, bool) {
	if !strings.EqualFold(call.name, "write") {
		return piToolCall{}, false
	}
	device, ok := strings.CutPrefix(getFirstStr(call.args, "path"), ompDeviceScheme)
	if !ok || device == "" || strings.Contains(device, "/") {
		return piToolCall{}, false
	}
	xdev := firstMap(call.details, "xdev")
	if xdev != nil {
		if getFirstStr(xdev, "mode") != "execute" {
			return piToolCall{}, false
		}
	} else if ompDeviceHelpContent.MatchString(getFirstStr(call.args, "content")) {
		return piToolCall{}, false
	}

	dispatched := piToolCall{name: device, args: firstMap(xdev, "args"), details: firstMap(xdev, "inner")}
	if dispatched.args == nil {
		// The tool_call and approval halves carry only the payload the model wrote. The result
		// carries the runtime's validated arguments instead, but not when validation is what failed.
		_ = json.Unmarshal([]byte(getFirstStr(call.args, "content")), &dispatched.args)
	}
	if dispatched.args == nil {
		dispatched.args = map[string]interface{}{}
	}
	return dispatched, true
}

// piFilePath returns the filesystem path a read, edit or write target names, or "" when the target
// is not a file.
//
// Oh My Pi's read and write tools take URIs as well as paths: `https://` fetches a web page, and its
// internal schemes -- `artifact://`, `agent://`, `skill://`, `local://`, `proc://`, `xd://` and the
// rest -- address runtime resources such as a spilled tool output, a subagent's report, a background
// job, or a tool device. None of those is file activity, and recording one under file.path would put
// a value that is not a filesystem path into the field every file rule, git helper and SIEM query
// treats as one; the goose mapper refuses web URLs there for the same reason, and the collector
// accepts only `file://` URIs. The runtime draws the same line: it reports a read's source as a
// `path`, a `url` or an `internal` resource. A `file://` URI is a path spelled as a URI, and is
// recorded as the path; see fileURLPath.
func piFilePath(target string) string {
	scheme, _, ok := strings.Cut(target, "://")
	if !ok || !isURIScheme(scheme) {
		return target
	}
	if !strings.EqualFold(scheme, "file") {
		return ""
	}
	return fileURLPath(target, runtime.GOOS)
}

// fileURLPath returns the path a `file://` URL names on goos, or "" when it names none.
//
// It follows Node's url.fileURLToPath, which is what these runtimes, written in TypeScript, use
// to turn the URL into the path they read. The two platforms disagree. On Windows,
// `file:///C:/Users/me/a.go` names `C:\Users\me\a.go` and `file://server/share/a.go` names the
// share `\\server\share\a.go`; a URL with no drive and no host names nothing. Elsewhere the
// URL's path is the path, and a URL with a host names nothing. An encoded separator never names a
// path on either. Taking the URL's path as written on Windows gave `/C:/Users/me/a.go`, which is
// not absolute there, and was then joined onto the working directory.
func fileURLPath(target, goos string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	host, escaped := parsed.Host, parsed.EscapedPath()
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	// `file://C:/a.go` puts the drive where a host would go; the URL standard reads it as the
	// path's drive letter.
	if len(host) == 2 && host[1] == ':' && isDriveLetter(host[:1]) {
		escaped, host = "/"+host+escaped, ""
	}
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%2f") || (goos == "windows" && strings.Contains(lower, "%5c")) {
		return ""
	}
	p, err := url.PathUnescape(escaped)
	if err != nil {
		return ""
	}
	if goos != "windows" {
		if host != "" {
			return ""
		}
		return p
	}
	p = strings.ReplaceAll(p, "/", `\`)
	if host != "" {
		return `\\` + host + p
	}
	if len(p) < 3 || !isDriveLetter(p[1:2]) || p[2] != ':' {
		return ""
	}
	return p[1:]
}

// isURIScheme reports whether s is an RFC 3986 scheme. A single letter is not accepted, so a
// Windows drive letter is never mistaken for one.
func isURIScheme(s string) bool {
	if len(s) < 2 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// filePath returns the file a read, edit or write call touched, or "" when its target is not a
// file.
//
// On a runtime that resolves its tool paths, the runtime's own statement of the file is preferred,
// because it is the one path that is certainly right: it is absolute, and any selector or `~` in
// what the model wrote has already been resolved. A result whose source the runtime reports as a
// URL or one of its own resources is not a file, whatever file backs it. A tool_call or an
// approval, which happen before the runtime has resolved anything, resolve the target the same way
// the runtime will.
func (f piFamily) filePath(call piToolCall, input map[string]interface{}, target string) string {
	if f.resolvesToolPaths {
		target = ompStripPathMarker(target)
	}
	path := piFilePath(target)
	if path == "" || !f.resolvesToolPaths {
		return path
	}
	resolved, isFile := piResolvedPath(call.details)
	if !isFile {
		return ""
	}
	if resolved != "" {
		return resolved
	}
	return ompToolPath(path, resolveCwd(input, f.platform), runtime.GOOS, ompIsWSL())
}

// piResolvedPath returns the absolute path a tool result reports having read or written, and
// whether the result describes a file at all.
//
// Oh My Pi reports the path in three places, one per tool: a read names its source in
// `meta.source`, a write names the file in `resolvedPath`, and an edit of one file names it in
// `path`. A read whose source is a URL or an `internal` resource is not a file read, even when a
// file in the runtime's own storage backs it and `resolvedPath` names that file.
func piResolvedPath(details map[string]interface{}) (string, bool) {
	var path string
	switch source := firstMap(firstMap(details, "meta"), "source"); getFirstStr(source, "type") {
	case "path":
		path = getFirstStr(source, "value")
	case "":
		path = getFirstStr(details, "resolvedPath", "path")
	default:
		return "", false
	}
	if !filepath.IsAbs(path) {
		return "", true
	}
	return path, true
}

// ompLineSelector is Oh My Pi's read selector grammar, FILE_LINE_RANGE_RE in its
// packages/tui/src/tools/read.ts: comma-joined line ranges (`10`, `10-40`, `10..40`, `10+30`,
// `10-`), a tail (`-60`), or `raw`, `conflicts` or `img`. A range may not end in `+`, which is
// what the runtime's trailing lookbehind enforces.
var ompLineSelector = regexp.MustCompile(`(?i)^(?:` + ompLineRange + `(?:,` + ompLineRange + `)*|-\d+|raw|conflicts|img)$`)

const ompLineRange = `L?\d+(?:(?:\.\.|-)(?:L?\d+)?|\+L?\d+)?`

// ompStrayColon matches the stray `:` some models put in front of a path (`:/abs`, `:../rel`,
// `:C:\repo`), which Oh My Pi drops before resolving: no real path starts with `:`.
var ompStrayColon = regexp.MustCompile(`^:(?:[/\\~]|\.\.?[/\\]|[A-Za-z]:)`)

// ompWindowsAbsolute matches a Windows absolute path: a drive (`C:\`, `C:/`), a UNC share, or a
// root-relative `\path`, the forms Node's path.win32.isAbsolute accepts.
var ompWindowsAbsolute = regexp.MustCompile(`^(?:[A-Za-z]:[\\/]|[\\/]{2}|\\)`)

// ompStripPathMarker drops the marker characters Oh My Pi strips from the front of a path before
// it reads one, mirroring the first steps of its expandPath (tools/path-utils.ts).
//
// A stray `:` goes before a path shape. A leading `@` -- a mention marker -- goes only before `/`,
// `~`, a Windows absolute path, or an internal URL, so a file literally named `@notes.md` keeps its
// name. The runtime recognizes an internal URL by its own scheme registry; any `scheme://` other
// than a file or web URL is taken for one here, because it is not a file either way. Stripping the
// `@` before the URI check is what keeps `@skill://name` from being read as a file path.
func ompStripPathMarker(target string) string {
	if ompStrayColon.MatchString(target) {
		target = target[1:]
	}
	rest, ok := strings.CutPrefix(target, "@")
	if !ok {
		return target
	}
	if strings.HasPrefix(rest, "/") || rest == "~" || strings.HasPrefix(rest, "~/") || ompWindowsAbsolute.MatchString(rest) {
		return rest
	}
	if scheme, _, ok := strings.Cut(rest, "://"); ok && isURIScheme(scheme) {
		switch strings.ToLower(scheme) {
		case "file", "http", "https":
		default:
			return rest
		}
	}
	return target
}

// ompUnicodeSpaces are the space characters Oh My Pi reads as a plain space in a path: no-break,
// the U+2000 block, narrow no-break, medium mathematical and ideographic.
var ompUnicodeSpaces = regexp.MustCompile("[\u00A0\u2000-\u200A\u202F\u205F\u3000]")

// ompToolPath turns a path as an Oh My Pi tool was given it into the file it names.
//
// Oh My Pi resolves a relative path against the session's working directory, expands `~`, and
// reads a trailing selector as a line range rather than part of the name: `read .env:1-20` reads
// the first twenty lines of `<cwd>/.env`. Recording the string as written put `.env:1-20` in
// file.path, which defeats every file rule that anchors its pattern at the end of the name -- the
// credential-read rules match `(^|/)\.env($|\.)`, not `.env:1-20` -- and filed one file under as
// many names as it was read with. Cline resolves relative paths for the same reason.
//
// The rest of the runtime's shorthand is applied the same way its expandPath and resolveToCwd
// apply it (tools/path-utils.ts), so the path recorded before a call is the one its result will
// report: Unicode spaces become plain spaces; `~name` is `<home>/name`, the runtime's reading
// rather than a shell's; a Windows drive alias is translated for the host (ompDriveAliasPath); a
// path that is nothing but slashes means the working directory, because the runtime reads `/` as
// "here" rather than the filesystem root; and on Windows a path that starts at a root but names no
// drive (`\Users\me\a.go`) is already absolute to the runtime -- Node's path.isAbsolute says so --
// and lands on the working directory's drive rather than under the working directory.
//
// The selector is peeled the way the runtime peels it: from the last colon, only when what follows
// is selector grammar, at most twice (a range and `raw` may be combined), and not at all when a
// file of that literal name exists, which is the runtime's own tiebreak.
//
// What this cannot reproduce is anything the runtime decides by looking at the disk once the path
// it resolved turns out not to exist -- a unique-suffix search under the working directory, an
// edit rebound by its snapshot tag, or a macOS spelling variant of the name. Those reach only the
// result, which reports the file the runtime actually used.
func ompToolPath(target, cwd, goos string, wsl bool) string {
	p := ompUnicodeSpaces.ReplaceAllString(target, " ")
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	p = ompDriveAliasPath(p, goos, wsl)
	switch {
	case strings.Trim(p, "/") == "" && cwd != "":
		p = cwd
	case !filepath.IsAbs(p) && cwd != "" && goos == "windows" && (p[0] == '/' || p[0] == '\\'):
		p = filepath.VolumeName(cwd) + filepath.FromSlash(p)
	case !filepath.IsAbs(p) && cwd != "":
		p = filepath.Join(cwd, p)
	}
	stripped := p
	for range 2 {
		i := strings.LastIndexByte(stripped, ':')
		if i <= 0 || !ompLineSelector.MatchString(stripped[i+1:]) {
			break
		}
		stripped = stripped[:i]
	}
	if stripped == p {
		return p
	}
	if _, err := os.Lstat(p); err == nil {
		return p
	}
	return stripped
}

// ompDriveAliasPath translates a Windows drive alias for the host the runtime runs on, mirroring
// its normalizeWindowsDriveAliasPath: on Windows, an MSYS or WSL mount root (`/c/...`,
// `/mnt/c/...`) is the drive itself (`C:\...`); under WSL, a pasted Windows path (`C:\...`,
// `C:/...`) is that drive's mount (`/mnt/c/...`). Anywhere else the path is left alone.
//
// The WSL direction normalizes the Windows path first, as the runtime does with
// path.win32.normalize, so a `..` stops at the drive root: `C:\..\Windows` is `/mnt/c/Windows`,
// never a directory above the drive's mount.
func ompDriveAliasPath(p, goos string, wsl bool) string {
	switch {
	case goos == "windows":
		parts := strings.Split(p, "/")
		if len(parts) < 2 || parts[0] != "" {
			return p
		}
		var drive string
		tail := parts[2:]
		if isDriveLetter(parts[1]) {
			drive = strings.ToUpper(parts[1])
		} else if len(parts) >= 3 && strings.EqualFold(parts[1], "mnt") && isDriveLetter(parts[2]) {
			drive = strings.ToUpper(parts[2])
			tail = parts[3:]
		}
		if drive == "" {
			return p
		}
		return drive + `:\` + strings.Join(nonEmpty(tail), `\`)
	case wsl:
		match := ompWindowsDrivePath.FindStringSubmatch(strings.ReplaceAll(strings.TrimSpace(p), "/", `\`))
		if match == nil {
			return p
		}
		var segments []string
		for _, segment := range strings.Split(match[2], `\`) {
			switch segment {
			case "", ".":
			case "..":
				if len(segments) > 0 {
					segments = segments[:len(segments)-1]
				}
			default:
				segments = append(segments, segment)
			}
		}
		mount := "/mnt/" + strings.ToLower(match[1])
		if len(segments) == 0 {
			return mount
		}
		return mount + "/" + strings.Join(segments, "/")
	default:
		return p
	}
}

var ompWindowsDrivePath = regexp.MustCompile(`^([A-Za-z]):\\(.*)$`)

func isDriveLetter(s string) bool {
	return len(s) == 1 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
}

func nonEmpty(parts []string) []string {
	out := parts[:0:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ompIsWSL reports whether the hook runs under WSL, the way Oh My Pi's isWsl decides it.
func ompIsWSL() bool {
	return runtime.GOOS == "linux" && (os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "")
}

// ompMCPResourceScheme is the URI scheme through which Oh My Pi's read tool reads an MCP resource:
// `read mcp://<resource-uri>` asks whichever connected server lists that resource.
const ompMCPResourceScheme = "mcp://"

// ompMCPResourceURI returns the resource URI a `read mcp://<uri>` call asks for, or "".
//
// Such a read is an MCP resources/read, and is recorded as one: the same mcp.tool_invoked other
// runtimes record for a resource read, with the URI in mcp.resource.uri. The server cannot be
// named. The runtime picks it by matching the URI against every connected server and reports the
// choice only in a note its read tool discards. The runtime also sends a resource URI whose scheme
// it does not handle itself to MCP, but telling those apart from its own schemes would mean
// copying its scheme registry, so only the explicit `mcp://` form is recognized.
func ompMCPResourceURI(call piToolCall) string {
	if !strings.EqualFold(call.name, "read") {
		return ""
	}
	target := getFirstStr(call.args, "path")
	if len(target) <= len(ompMCPResourceScheme) || !strings.EqualFold(target[:len(ompMCPResourceScheme)], ompMCPResourceScheme) {
		return ""
	}
	return target[len(ompMCPResourceScheme):]
}

// toolFields builds the tool, command, and file blocks for one Pi-family tool event.
//
// The built-in tools have fixed, documented argument shapes -- bash takes `command`, and read,
// edit and write all take `path` -- so these are read by name rather than by guessing across
// spellings. A custom tool registered by another extension carries an arbitrary shape, and gets
// tool.name plus its raw arguments without a command or file block invented for it.
func (f piFamily) toolFields(call piToolCall, input map[string]interface{}, withResult bool) map[string]interface{} {
	name, args := call.name, call.args
	fields := map[string]interface{}{}
	tool := map[string]interface{}{}
	if name != "" {
		tool["name"] = name
	}

	switch strings.ToLower(name) {
	case "bash":
		if command := getFirstStr(args, "command"); command != "" {
			tool["command"] = command
			fields["command"] = map[string]interface{}{"command": command}
			fields["content"] = retainedContentFields(command)
		}
	// Prime Agent's only default tool. It executes Python in a persistent kernel, and that kernel
	// is where essentially all of its agent activity happens: it reads and writes files, and it
	// runs shell commands through a `bash()` helper rather than through a separate shell tool. So
	// the code is recorded in the command category rather than as an anonymous tool call, for the
	// reason Oh My Pi's operator `$` code is -- `os.system("rm -rf /")` is a shell command wearing
	// a Python hat, and the risky-command rules all match on `command.command`. It is marked as
	// Python in `raw` rather than passed off as a shell command.
	//
	// Pi and Oh My Pi never reach this case: neither ships a tool by this name.
	case "ipython":
		if code := getFirstStr(args, "code"); code != "" {
			tool["command"] = code
			fields["command"] = map[string]interface{}{"command": code}
			fields["content"] = retainedContentFields(code)
		}
	case "read", "edit", "write":
		// tool.path keeps the target as the tool was given it, URI and selector included, so the
		// row still says what was asked for; only a file becomes a file block, under its real path.
		if target := getFirstStr(args, "path"); target != "" {
			tool["path"] = target
			if path := f.filePath(call, input, target); path != "" {
				fields["file"] = map[string]interface{}{
					"path":      path,
					"operation": piFileOperation(name),
					"language":  strings.TrimPrefix(filepath.Ext(path), "."),
				}
			}
		}
		if uri := ompMCPResourceURI(call); uri != "" {
			fields["mcp"] = map[string]interface{}{
				"method":   map[string]interface{}{"name": "resources/read"},
				"resource": map[string]interface{}{"uri": uri},
			}
		}
	}

	if len(tool) > 0 {
		fields["tool"] = tool
	}
	f.applyMCPAttribution(fields, call)
	if withResult {
		if usage := piUsage(firstMap(input, "usage")); len(usage) > 0 {
			fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{"usage": usage})
		}
	}
	return fields
}

// piFileOperation maps a Pi-family file tool onto the operation vocabulary the event schema uses.
func piFileOperation(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "write":
		return "create"
	default:
		return "modify"
	}
}

// applyMCPAttribution fills the `mcp` block when the call is known to have reached an MCP server.
//
// Without it an MCP call lands in the log as a tool named `mcp__github_create_issue` and nothing
// else -- no server, no tool -- so the two questions actually asked about MCP activity ("which
// server did this agent reach, and what did it call there") have no field to answer them.
func (f piFamily) applyMCPAttribution(fields map[string]interface{}, call piToolCall) {
	server, tool := piMCPServerTool(call)
	if server == "" && tool == "" {
		return
	}
	mcp := map[string]interface{}{}
	if server != "" {
		mcp["server"] = server
	}
	if tool != "" {
		mcp["tool"] = tool
	}
	fields["mcp"] = mergeNested(fields["mcp"], mcp)
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"operation": map[string]interface{}{"name": "execute_tool"},
	})
}

// piMCPServerTool returns the MCP server and tool a Pi-family call reached, when that is known.
//
// The runtime's own statement wins, as it does in the shared hook mapper and the collector: Oh My
// Pi's MCP results carry `serverName` and `mcpToolName` in their details, on failures as well as
// successes, and those are the server's configured name and the tool's own name exactly.
//
// Without them only the reversible `mcp__<server>__<tool>` spelling is read. Oh My Pi's own names
// are not reversible, so a tool_call or approval that carries nothing but one is given no server
// rather than a guessed one. The runtime mints `mcp__<server>_<tool>` by lowercasing both halves,
// turning every other character run into `_`, dropping a tool's redundant server prefix, and
// hashing names past 64 characters -- so `beacon-managed`'s `beacon_lookup` becomes
// `mcp__beacon_managed_beacon_lookup`, and no split of that string recovers the server. Splitting at
// the first underscore reported it as server `beacon`, the name of Beacon's own local server. The
// call's result, joined to it by gen_ai.tool.call.id, names the server.
func piMCPServerTool(call piToolCall) (string, string) {
	server := getFirstStr(call.details, "serverName")
	tool := getFirstStr(call.details, "mcpToolName")
	if server != "" && tool != "" {
		return server, tool
	}
	return deriveMCPServerTool(call.name)
}

// piIsMCPTool reports whether a call is MCP activity, whether or not its server is known.
//
// `mcp__` is the namespace Oh My Pi reserves for MCP tools, and the test its own isMCPToolName
// applies, so a call carrying the prefix is MCP activity even where only its result can say which
// server it reached.
func piIsMCPTool(call piToolCall) bool {
	if server, tool := piMCPServerTool(call); server != "" || tool != "" {
		return true
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(call.name), "mcp__")
	return ok && rest != ""
}

// toolResultEvents maps a completed tool call onto its outcome event.
func (f piFamily) toolResultEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	call := piToolCallOf(input)
	mergeMap(fields, f.toolFields(call, input, true))
	applyToolCallID(fields, input)
	name := call.name
	f.applyPythonMarker(fields, name)

	// Both of these read Prime Agent's `ipython` result and are no-ops for every other tool, so Pi
	// and Oh My Pi never reach them. They run before the failure branch below because a cell that
	// raised still executed the statements before the one that raised: its output and the files it
	// wrote are exactly what an investigation into a failed cell reads.
	f.applyPythonResult(fields, name, input)
	kernelWrites := f.pythonDiffEvents(name, input, fields)

	if isErr, ok := input["isError"].(bool); ok && isErr {
		fields["error"] = map[string]interface{}{"type": "tool_error"}
		return append(f.one("tool.failed", "tool", "high", "tool failed", fields), kernelWrites...)
	}

	if edits := f.perFileEditEvents(call, fields); len(edits) > 0 {
		return append(edits, kernelWrites...)
	}

	if diff := piEditDiff(call.details); diff != "" {
		fields["content"] = retainedContentFields(diff)
	}

	action, category := piToolAction(call)
	// A file action with no file is not a file action. The read tool accepts a path that failed to
	// resolve and a target that is not a file at all, and a custom tool can share a built-in's name,
	// so reporting file.read with no file field would produce a row every file-scoped query matches
	// and none can explain -- the same guard clineToolAfterEvents applies for the same reason.
	if strings.HasPrefix(action, "file.") {
		if _, ok := fields["file"]; !ok {
			action, category = "tool.completed", "tool"
		}
	}
	if action == "command.executed" {
		if _, ok := fields["command"]; !ok {
			action, category = "tool.completed", "tool"
		}
	}
	return append(f.one(action, category, "info", piToolMessageSuffix(action), fields), kernelWrites...)
}

// applyPythonMarker notes that a command block holds Python rather than a shell command line.
//
// Applied to the caller's fields rather than inside toolFields, which returns a fresh map that
// mergeMap copies over the caller's -- writing `raw` there would replace the verbatim payload block
// with this one key. Prime Agent's `ipython` cell is executed as literally as a shell command
// (`os.system("rm -rf /")` is a shell command wearing a Python hat), which is why it is recorded in
// the command category at all; the marker is what keeps a reader from taking command.command for a
// shell line.
func (f piFamily) applyPythonMarker(fields map[string]interface{}, toolName string) {
	if strings.ToLower(toolName) != "ipython" {
		return
	}
	if _, ok := fields["command"]; !ok {
		return
	}
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("python"): true})
}

// applyPythonResult records what a Prime Agent `ipython` cell did, from the details it reports.
//
// Without this an ipython result is a command with no outcome at all: no output, no duration, and
// no sign of whether the cell raised. That is the whole of Prime Agent's tool surface, so the gap
// would not be one tool missing its result -- it would be every agent action missing it.
//
// Nothing here is derived. `status` is the runtime's own word, the duration is its own measurement,
// and the output is the streams it captured. There is no exit code to record, because a Python
// cell does not have one; inventing a 0/1 from `status` would put a shell's vocabulary on something
// that is not a shell.
func (f piFamily) applyPythonResult(fields map[string]interface{}, toolName string, input map[string]interface{}) {
	if strings.ToLower(toolName) != "ipython" {
		return
	}
	details := firstMap(input, "details")
	if details == nil {
		return
	}

	// stdout first, then stderr, then the cell's value. The order is the order a reader of the
	// terminal saw them in, and each is included only when the runtime captured something, so a
	// cell that printed nothing yields no output field rather than a string of blank lines.
	var streams []string
	for _, key := range []string{"stdout", "stderr", "result"} {
		if text := getFirstStr(details, key); text != "" {
			streams = append(streams, text)
		}
	}
	if len(streams) > 0 {
		output := strings.Join(streams, "\n")
		command := mutableChild(fields["command"])
		command["output"] = output
		fields["command"] = command
		// The retention marker on the code is the more specific claim -- it is what the rules match
		// on -- so output only fills the field when nothing has claimed it, the same precedence
		// applyKiroToolResult uses.
		if _, exists := fields["content"]; !exists {
			fields["content"] = retainedContentFields(output)
		}
	}

	if duration, ok := firstToolIntAcross([]map[string]interface{}{details}, "durationMs", "duration_ms"); ok {
		command := mutableChild(fields["command"])
		command["duration_ms"] = duration
		fields["command"] = command
	}

	raw := map[string]interface{}{}
	// ok, error or aborted. "aborted" is the one worth keeping separately from isError: a cell the
	// operator interrupted is not a cell that failed, and a log that cannot tell them apart reports
	// every Ctrl+C as an agent error.
	if status := getFirstStr(details, "status"); status != "" {
		raw["python_status"] = status
	}
	// The exception class, when the cell raised one. The message and traceback are deliberately not
	// promoted: they are already in the verbatim payload under `raw`, and a traceback quotes source
	// lines, which is content rather than a fact about the event.
	if errName := getFirstStr(details, "errorEname"); errName != "" {
		raw["python_error"] = errName
	} else if errDetail := firstMap(details, "error"); errDetail != nil {
		if errName := getFirstStr(errDetail, "ename"); errName != "" {
			raw["python_error"] = errName
		}
	}
	// A restarted kernel has lost every variable, import and open handle the session had built up,
	// so a reader comparing two cells across that boundary is comparing two different processes.
	if restarted, ok := details["kernelRestarted"].(bool); ok && restarted {
		raw["kernel_restarted"] = true
	}
	if len(raw) == 0 {
		return
	}
	prefixed := map[string]interface{}{}
	for key, value := range raw {
		prefixed[f.rawKey(key)] = value
	}
	fields["raw"] = mergeNested(fields["raw"], prefixed)
}

// pythonDiffEvents turns the file edits a Prime Agent kernel cell reported into file events.
//
// Prime Agent's kernel streams a diff display for every file its helpers rewrite, so a cell that
// edits three files reports three of them. Each becomes its own file.modified, the same one-event-
// per-changed-file shape the OpenHands mapper produces for one apply_patch call: a store that files
// one row per path is the one a "who touched this file" query can answer.
//
// The pair is a replaced span rather than the file before and after -- the kernel names the text it
// matched and the text it substituted -- so the patch is built with FromEditFragments rather than
// FromContentChange. Choosing the wrong one there produces a diff that is wrong rather than absent.
func (f piFamily) pythonDiffEvents(toolName string, input, fields map[string]interface{}) []normalizedEvent {
	if strings.ToLower(toolName) != "ipython" {
		return nil
	}
	details := firstMap(input, "details")
	if details == nil {
		return nil
	}
	diffs, ok := details["diffs"].([]interface{})
	if !ok {
		return nil
	}

	var events []normalizedEvent
	for _, item := range diffs {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		path := getFirstStr(entry, "path")
		if path == "" {
			continue
		}
		// FromEditFragments returns nothing when the replaced span is identical to its
		// replacement, which is an edit that changed nothing on disk. diffFields would still build
		// a file block from the path alone, so the emptiness is checked here rather than there --
		// otherwise a no-op edit is recorded as a modification that never happened.
		patch := diff.FromEditFragments(path, getFirstStr(entry, "oldStr"), getFirstStr(entry, "newStr"))
		if patch == "" {
			continue
		}
		fileFields := diffFields(path, patch)
		if fileFields == nil {
			continue
		}

		values := cloneFields(fields)
		// The cell's code stays out of the file event. It is already on the command.executed this
		// event accompanies, joined to it by the same tool call id, and carrying it here would put
		// a whole Python cell in `command.command` on a row whose subject is one file -- where a
		// command-scoped rule would then match the cell once per file it happened to touch.
		delete(values, "command")
		mergeMap(values, fileFields)
		events = append(events, f.one("file.modified", "file", "info", "file modified", values)...)
	}
	return events
}

// piEditDiff returns the unified patch the edit tool reports, when it reported one.
//
// EditToolDetails carries both a display-oriented `diff` and a standard unified `patch`. The patch
// is preferred because it is the machine-readable one; the diff is a fallback for a details object
// that carried only the display form.
func piEditDiff(details map[string]interface{}) string {
	return getFirstStr(details, "patch", "diff")
}

// perFileEditEvents records an edit that changed several files as one file.modified per file.
//
// Oh My Pi's default edit format applies one patch to any number of files. An edit of one file
// reports that file's path; an edit of several reports no path at all, only a perFileResults list.
// Without this, such an edit fell through to tool.completed and none of the files it changed was
// recorded. One event per changed file is the shape pythonDiffEvents produces for a Prime Agent
// cell and the OpenHands mapper produces for one apply_patch call: a store that files one row per
// path is the one a "who touched this file" query can answer.
//
// Each file keeps the edit's own operation word, so a file the edit created or deleted reads as
// one, while the action stays in the file.modified family as a single-file edit's does.
func (f piFamily) perFileEditEvents(call piToolCall, fields map[string]interface{}) []normalizedEvent {
	if !strings.EqualFold(call.name, "edit") {
		return nil
	}
	results, _ := call.details["perFileResults"].([]interface{})
	var events []normalizedEvent
	for _, item := range results {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		path := getFirstStr(entry, "path")
		if !filepath.IsAbs(path) {
			continue
		}
		values := cloneFields(fields)
		values["file"] = map[string]interface{}{
			"path":      path,
			"operation": ompEditOperation(getFirstStr(entry, "op")),
			"language":  strings.TrimPrefix(filepath.Ext(path), "."),
		}
		delete(values, "content")
		if diff := piEditDiff(entry); diff != "" {
			values["content"] = retainedContentFields(diff)
		}
		events = append(events, f.one("file.modified", "file", "info", "file modified", values)...)
	}
	return events
}

// ompEditOperation maps the operation Oh My Pi reports for one file of an edit onto the event
// schema's vocabulary.
func ompEditOperation(op string) string {
	switch strings.ToLower(op) {
	case "create":
		return "create"
	case "delete":
		return "delete"
	default:
		return "modify"
	}
}

// piToolAction maps a Pi-family tool call onto the endpoint action its completion represents.
func piToolAction(call piToolCall) (string, string) {
	switch strings.ToLower(call.name) {
	case "bash", "ipython":
		return "command.executed", "command"
	case "read":
		if ompMCPResourceURI(call) != "" {
			return "mcp.tool_invoked", "mcp"
		}
		return "file.read", "file"
	case "edit":
		return "file.modified", "file"
	case "write":
		return "file.created", "file"
	default:
		// An MCP-routed tool is real, attributable activity rather than an anonymous custom tool,
		// and mcp.tool_invoked is the action every other Beacon capture path already uses for it --
		// so an MCP call through Oh My Pi joins the same rows a detection reads for Cline, Cursor
		// and Claude Code rather than hiding under tool.completed.
		if piIsMCPTool(call) {
			return "mcp.tool_invoked", "mcp"
		}
		// grep, glob, and any tool another extension registered. These are real tool activity with
		// no file or command semantics worth asserting: grep takes a pattern, not a path, and a
		// custom tool's arguments mean whatever its author decided.
		return "tool.completed", "tool"
	}
}

// piToolMessageSuffix returns the runtime-independent half of a tool event's message.
func piToolMessageSuffix(action string) string {
	switch action {
	case "command.executed":
		return "command executed"
	case "file.read":
		return "file read"
	case "file.created":
		return "file created"
	case "file.modified":
		return "file modified"
	case "tool.failed":
		return "tool failed"
	case "mcp.tool_invoked":
		return "MCP tool invoked"
	default:
		return "tool completed"
	}
}

// userMessagePromptEvents records a user message as a submitted prompt when the extension has
// marked it as one by attaching its text under `prompt`.
//
// The extension makes that call because only it has the state to: interactive and RPC sessions
// report every submission as `input` first, and recording the user message there as well would
// count each prompt twice. On print and ACP, which never emit `input`, the user message is the
// only record of what was asked. A user message without `prompt` -- every one an older extension
// forwarded -- produces nothing.
//
// The text is the message Oh My Pi delivered to the model, after it expanded slash commands,
// prompt templates and model mentions, not the keystrokes an `input` event carries. The origin is
// recorded so a reader can tell the two shapes apart.
func (f piFamily) userMessagePromptEvents(input, fields map[string]interface{}) []normalizedEvent {
	prompt := getFirstStr(input, "prompt")
	if prompt == "" {
		return nil
	}
	fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{f.rawKey("prompt_origin"): "message_end"})
	events := []normalizedEvent{f.promptEvent(fields, prompt, "")}
	if link, ok := handoffLinkEvent(fields, prompt); ok {
		events = append(events, link)
	}
	return events
}

// messageEndEvents records what a finished assistant message tells us: its token usage, and the
// model's reasoning when the provider returned any. A user message is a prompt; see
// userMessagePromptEvents.
//
// A finalized message is the only place these runtimes report usage, and message_end fires for
// toolResult messages too, so a message with neither usage nor reasoning produces nothing rather
// than an empty row per turn.
func (f piFamily) messageEndEvents(input map[string]interface{}, fields map[string]interface{}) []normalizedEvent {
	message := firstMap(input, "message")
	if message == nil {
		return nil
	}
	role := getFirstStr(message, "role")
	if role == "user" {
		return f.userMessagePromptEvents(input, fields)
	}
	if role != "assistant" {
		return nil
	}
	if model := getFirstStr(message, "model", "responseModel"); model != "" {
		fields["model"] = model
	}

	var events []normalizedEvent

	if reasoning := piReasoningText(message); reasoning != "" {
		reasoningFields := cloneFields(fields)
		reasoningFields["gen_ai"] = mergeNested(reasoningFields["gen_ai"], map[string]interface{}{
			"output": map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{
					"role":  "assistant",
					"parts": []interface{}{map[string]interface{}{"type": "reasoning", "content": reasoning}},
				}},
			},
		})
		reasoningFields["content"] = retainedContentFields(reasoning)
		events = append(events, f.one("agent.reasoning", "reasoning", "info", "agent reasoning", reasoningFields)...)
	}

	if usage := piUsage(firstMap(message, "usage")); len(usage) > 0 {
		usageFields := cloneFields(fields)
		usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{"usage": usage})
		events = append(events, f.one("token.usage", "metric", "info", "token usage", usageFields)...)
	}

	return events
}

// piReasoningText concatenates the thinking parts of an assistant message.
//
// Assistant content is a list of parts, and a reasoning model emits thinking alongside text in the
// same message. Only the thinking parts are collected here: the assistant's visible answer is not
// reasoning, and recording it as such would put the model's output where a reader looking for its
// private deliberation expects to find it.
func piReasoningText(message map[string]interface{}) string {
	content, ok := message["content"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range content {
		part, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if getFirstStr(part, "type") != "thinking" {
			continue
		}
		if text := getFirstStr(part, "thinking", "text", "content"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// piUsage normalizes a Pi-family Usage object into gen_ai.usage.
//
// These runtimes name their fields input/output/cacheRead/cacheWrite/reasoning and nest cost under
// `cost`, none of which match the OTel GenAI semconv names Beacon writes, and two of which are
// nested objects on Beacon's side rather than scalars. The mapping is spelled out against the
// canonical GenAIUsageInfo shape rather than copied through, so gen_ai.usage stays the only token
// representation in the log and no parallel per-harness field appears beside it.
//
// `output` already includes `reasoning` tokens, so reasoning is recorded under its own key but
// never added to anything: treating it as a separate bucket would double-count it in any total.
// A `totalTokens` is also reported and deliberately dropped -- Beacon's usage shape has no total,
// and a redundant field that can disagree with its own parts is worse than an absent one.
func piUsage(usage map[string]interface{}) map[string]interface{} {
	if usage == nil {
		return nil
	}
	sources := []map[string]interface{}{usage}
	out := map[string]interface{}{}
	if value, ok := firstToolIntAcross(sources, "input"); ok {
		out["input_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "output"); ok {
		out["output_tokens"] = value
	}
	if value, ok := firstToolIntAcross(sources, "cacheRead"); ok {
		out["cache_read"] = map[string]interface{}{"input_tokens": value}
	}
	if value, ok := firstToolIntAcross(sources, "cacheWrite"); ok {
		creation := map[string]interface{}{"input_tokens": value}
		// cacheWrite1h is the subset of cacheWrite written with one-hour retention, which Pi fills
		// only for Anthropic (billed at 2x input against 1.25x for five-minute writes). It is a
		// breakdown inside cache_creation, never a count of its own, and cannot exceed cacheWrite.
		if oneHour, ok := firstToolIntAcross(sources, "cacheWrite1h"); ok {
			creation["ephemeral_1h_input_tokens"] = min(max(oneHour, 0), max(value, 0))
		}
		out["cache_creation"] = creation
	}
	if value, ok := firstToolIntAcross(sources, "reasoning"); ok {
		out["reasoning"] = map[string]interface{}{"output_tokens": value}
	}
	// Runtime-reported cost only. Beacon never derives cost from a local pricing table, so a build
	// or provider that reports no cost leaves the field absent rather than estimated.
	if cost := firstMap(usage, "cost"); cost != nil {
		if value, ok := jsonFloat(cost["total"]); ok {
			out["cost_usd"] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
