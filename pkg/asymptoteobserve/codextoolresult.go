package asymptoteobserve

import (
	"encoding/json"
	"strconv"
	"strings"
)

// CodexToolResult is one Codex tool call and its result, as either capture path reads it: the live
// OTLP `codex.tool_result` record, or the rollout's `function_call` / `custom_tool_call` and its
// `*_output` that `beacon endpoint codex sync` reads afterwards.
//
// Both paths describe the same call, so they hand what they read to NormalizeCodexToolResult and
// apply what it returns, rather than each deciding what a Codex tool result means. Two readings of
// one record had drifted apart before this existed: the live path recorded no output and the
// arguments JSON as the command, the session path recorded output and the command line, and the
// two copies of every shell call never recognized each other as one call.
type CodexToolResult struct {
	// Name is the tool's own name, without its namespace: `exec_command`, or the sanitized name of
	// an MCP server's tool (`save_note`).
	Name string
	// Namespace is the namespace Codex gives the tool: `mcp__<server>` for an MCP server's tools,
	// `functions` and others for Codex's own. OTLP `tool_namespace`; the rollout's `namespace`.
	Namespace string
	CallID    string
	// Arguments is the decoded argument object or its JSON text.
	Arguments interface{}
	// Output is the text Codex gave the model, verbatim. For exec_command that includes Codex's
	// header (`Chunk ID`, `Wall time`, `Process exited with code N`, ...) above `Output:`.
	Output string
	// OutputTruncated is Codex's own report that it shortened Output (OTLP `output_truncated`).
	OutputTruncated bool
	// Failed is the runtime's own report that the call failed: OTLP `success=false`, or a rollout
	// status of failed/error.
	Failed     bool
	DurationMS int64
}

// CodexToolResultFields is what a Codex tool result sets on its event. Each capture path copies
// these onto its own event type; nil fields are left unset.
type CodexToolResultFields struct {
	Action    string
	Category  string
	Message   string
	Severity  Severity
	Tool      *ToolInfo
	Command   *CommandInfo
	MCP       *MCPInfo
	GenAITool *GenAIToolInfo
	Content   *ContentInfo
}

// codexMCPNamespacePrefix starts the namespace Codex gives every tool of an MCP server:
// `mcp__notes` for the server `notes`, spelled the way Claude Code prefixes its MCP tool names.
const codexMCPNamespacePrefix = "mcp__"

// NormalizeCodexToolResult says what a Codex tool result is:
//
//   - An MCP server's tool is mcp.tool_invoked, with mcp.server and mcp.tool, and named
//     `mcp__<server>__<tool>` in tool.name and gen_ai.tool.name, the spelling the ingested-content
//     scope and every other runtime's MCP events use. The server is read from the namespace on
//     both paths, because the rollout carries nothing else: an OTLP record's mcp_server attribute
//     names the same server unsanitized, and using it would leave the two copies of one call
//     with different targets. A call reported with no namespace under the already-qualified name
//     is read from that name.
//   - A shell tool is command.executed with command.command, command.output, and the exit code
//     Codex wrote into the output's header. A nonzero exit code is a failure even where the
//     runtime reported none, because the rollout records no success flag at all.
//   - Any other tool is tool.completed, or tool.failed when it failed.
//
// Every result but a shell command's is gen_ai.tool.call.result, which a shell command's output
// would only repeat. That includes tools whose output is not outside content -- write_stdin
// carries a long-running command's later output and exit status -- because the rules engine
// decides for itself which results it reads (the shared ingested-content scope).
func NormalizeCodexToolResult(r CodexToolResult) CodexToolResultFields {
	name := strings.TrimSpace(r.Name)
	namespace := strings.TrimSpace(r.Namespace)
	args := codexArguments(r.Arguments)
	out := CodexToolResultFields{Severity: SeverityInfo}
	if r.Failed {
		out.Severity = SeverityMedium
	}

	server, mcpTool, isMCP := codexMCPTool(name, namespace)
	toolName := CodexToolName(name, namespace)
	out.Tool = &ToolInfo{Name: toolName}
	out.GenAITool = &GenAIToolInfo{Name: toolName, Call: &GenAIToolCallInfo{ID: strings.TrimSpace(r.CallID), Arguments: args}}

	switch {
	case isMCP:
		out.Action, out.Category, out.Message = "mcp.tool_invoked", "mcp", "Codex MCP tool completed"
		out.MCP = &MCPInfo{Server: server, Tool: mcpTool}
	case IsShellCommandTool(name):
		out.Action, out.Category, out.Message = "command.executed", "command", "Codex command completed"
		command := codexCommandLine(name, args)
		out.Tool.Command = command
		out.Command = &CommandInfo{Command: command, Output: r.Output, DurationMS: r.DurationMS}
		if code, ok := codexExitCode(r.Output); ok {
			out.Command.ExitCode = &code
			if code != 0 {
				out.Severity = SeverityMedium
			}
		}
		out.Content = codexRetainedOutput(r.Output, DefaultStringLimit, r.OutputTruncated)
	case r.Failed:
		out.Action, out.Category, out.Message = "tool.failed", "tool", "Codex tool failed"
	default:
		out.Action, out.Category, out.Message = "tool.completed", "tool", "Codex tool completed"
	}

	if r.Output != "" && out.Command == nil {
		out.GenAITool.Call.Result = r.Output
		// Both writers store gen_ai at the raw-attribute limit, so that is the limit the marker
		// has to answer against.
		out.Content = codexRetainedOutput(r.Output, DefaultRawStringLimit, r.OutputTruncated)
	}
	return out
}

// CodexToolName is the name Beacon records for a Codex tool: `mcp__<server>__<tool>` for an MCP
// server's tool, which Codex reports as a bare name beside its `mcp__<server>` namespace, and the
// bare name for every other tool. Without the namespace an MCP call is indistinguishable from a
// built-in, and the ingested-content scope never matches it.
func CodexToolName(name, namespace string) string {
	name, namespace = strings.TrimSpace(name), strings.TrimSpace(namespace)
	if _, ok := codexMCPServer(namespace); ok && name != "" {
		return namespace + "__" + name
	}
	return name
}

// codexMCPTool is the MCP server and tool a Codex call names: from its `mcp__<server>` namespace,
// or, when it has none, from a name already spelled `mcp__<server>__<tool>`.
func codexMCPTool(name, namespace string) (server, tool string, ok bool) {
	if server, ok := codexMCPServer(namespace); ok {
		return server, name, true
	}
	if namespace != "" {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(name, codexMCPNamespacePrefix)
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(rest, "__")
	return server, tool, ok && server != "" && tool != ""
}

func codexMCPServer(namespace string) (string, bool) {
	server, ok := strings.CutPrefix(strings.TrimSpace(namespace), codexMCPNamespacePrefix)
	return server, ok && server != ""
}

// codexExitCodePrefixes are the header lines that carry a finished command's exit code:
// exec_command's `Process exited with code N` (a process still running when the call returns
// gets `Process running with session ID N` instead, and no exit code), and the `Exit code: N`
// that shell and shell_command write.
var codexExitCodePrefixes = []string{"Process exited with code ", "Exit code: "}

// codexExitCode reads the exit code from the header Codex puts above a command's output. Only the
// header is read -- the lines before `Output:` -- so a command that prints a line of the same
// shape is never taken for its own exit status.
func codexExitCode(output string) (int, bool) {
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if line == "Output:" {
			return 0, false
		}
		for _, prefix := range codexExitCodePrefixes {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				code, err := strconv.Atoi(rest)
				return code, err == nil
			}
		}
	}
	return 0, false
}

// codexArguments is the argument object decoded, when the runtime reported it as JSON text.
func codexArguments(args interface{}) interface{} {
	text, ok := args.(string)
	if !ok {
		return args
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var decoded interface{}
	if json.Unmarshal([]byte(text), &decoded) == nil {
		return decoded
	}
	return text
}

// codexCommandLine is a shell tool's command line: the command argument, or the input itself for
// a freeform shell tool whose input is the command line rather than an argument object.
func codexCommandLine(name string, args interface{}) string {
	if command := ToolCommandLine(name, args); command != "" {
		return command
	}
	if text, ok := args.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func codexRetainedOutput(output string, storeLimit int, truncatedAtSource bool) *ContentInfo {
	content := RetainedContent(output, storeLimit)
	if content != nil && truncatedAtSource {
		content.Truncated = true
	}
	return content
}
