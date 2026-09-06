package beaconevent

import (
	"net/url"
	"strings"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Grok Bot is xAI's always-on agent product. Each Bot runs on a Cursor-hosted cloud computer,
// and the desktop app is a client to it, so there is no hook, plugin, or session store on the
// endpoint for Beacon to read. The one telemetry channel is Cursor's server-side OpenTelemetry
// export: OTLP/HTTP logs and metrics pushed from Cursor's infrastructure to a collector the
// customer runs, with the resource attribute cursor.surface=grok_bot marking Bot traffic and the
// log body carrying the event name (grok_bot_shell_command, grok_bot_mcp_tool_call, ...).
//
// The shapes below follow Cursor's published wire reference for that export. Every attribute is
// vendor-prefixed and none of them appear on any other runtime, so this normalizer is selected by
// the surface attribute alone and is a no-op for everything else -- including Cursor's other
// surfaces (desktop, cli, cloud_agent, bugbot), which keep the passthrough `cursor` name.
//
// What is deliberately not done here: no approval is synthesized. Cursor names the shell-policy
// decision itself on every shell event (cursor.grok_bot.shell.allowed), so a blocked command is
// an observed denial, not one Beacon derived. Tool arguments, results, prompts, screenshots, and
// coordinates never leave Cursor's side of the export, so none of those fields are promoted or
// invented.

const (
	cursorSurfaceKey     = "cursor.surface"
	cursorSurfaceGrokBot = "grok_bot"

	cursorGrokBotShellCommand       = "grok_bot_shell_command"
	cursorGrokBotMCPToolCall        = "grok_bot_mcp_tool_call"
	cursorGrokBotBrowserNavigation  = "grok_bot_browser_navigation"
	cursorGrokBotComputerUseSession = "grok_bot_computer_use_session"
	cursorAPIRequest                = "api_request"
	cursorAPIError                  = "api_error"
)

// IsCursorGrokBotSurface reports whether the merged attributes carry Cursor's marker for Grok
// Bot traffic. It is the sole selector for the Grok Bot harness on the OTLP path: the export's
// service.name is `cursor` for every surface, so the service name alone cannot tell a Bot's
// cloud-computer shell command from a Cursor IDE session.
func IsCursorGrokBotSurface(attrs map[string]interface{}) bool {
	return strings.EqualFold(strings.TrimSpace(FirstString(attrs, cursorSurfaceKey)), cursorSurfaceGrokBot)
}

// CursorLogEventName reads the event name Cursor's export writes in the log body, falling back to
// an event.name attribute for a collector pipeline that has already moved it there.
func CursorLogEventName(attrs map[string]interface{}, body string) string {
	return strings.ToLower(strings.TrimSpace(FirstNonEmpty(body, FirstString(attrs, "event.name"))))
}

// NormalizeCursorGrokBotLogEvent maps one Grok Bot log record onto Beacon's event vocabulary.
// Runs after InferActionWithFidelity, whose keyword fallback has no idea what a
// `grok_bot_shell_command` body is; every arm below is selected by an exact body match, so the
// action it settles on is observed rather than inferred. An unrecognized body keeps the fallback
// action and its inferred fidelity, which is the honest reading of a Cursor event Beacon has not
// seen before.
func (c Converter) NormalizeCursorGrokBotLogEvent(event *Event, attrs map[string]interface{}, body string) {
	if event == nil || event.Harness.Name != "grok_bot" {
		return
	}
	applyCursorGrokBotContext(event, attrs)
	observed := asymptoteobserve.FidelityObserved
	switch CursorLogEventName(attrs, body) {
	case cursorGrokBotShellCommand:
		command := FirstString(attrs, "cursor.grok_bot.shell.command")
		target := strings.ToLower(FirstString(attrs, "cursor.grok_bot.shell.target"))
		// The Bot's cloud computer is the default; only a command Cursor says ran on the member's
		// own desktop through the local-execution helper is local. That is the one Grok Bot action
		// that touches an endpoint, and origin is how a reader finds it.
		if target == "user_machine" {
			event.Origin = asymptoteobserve.OriginLocal
		}
		if allowed, ok := BoolAttr(attrs, "cursor.grok_bot.shell.allowed"); ok && !allowed {
			// Cursor's shell policy stopped the command before it ran. The decision is Cursor's own
			// field, not something read off an adjacent event, so it is recorded as an observed
			// denial rather than withheld.
			event.Event.Action = "approval.denied"
			event.Event.Category = "approval"
			event.Approval = &ApprovalInfo{Required: true, Decision: "denied", Reason: FirstString(attrs, "cursor.grok_bot.shell.blocked_reason")}
			if event.Severity == "info" {
				event.Severity = "medium"
			}
			event.Message = "Grok Bot shell command blocked by policy"
		} else {
			event.Event.Action = "command.executed"
			event.Event.Category = "command"
			event.Message = "Grok Bot shell command executed"
		}
		event.Event.Fidelity = observed
		if command != "" {
			event.Command = &CommandInfo{Command: command}
			event.Tool = &ToolInfo{Name: "shell", Command: command}
		}
	case cursorGrokBotMCPToolCall:
		tool := FirstString(attrs, "cursor.tool.name")
		server := FirstString(attrs, "cursor.mcp.server.name")
		if strings.EqualFold(FirstString(attrs, "cursor.tool.status"), "failure") {
			event.Event.Action = "tool.failed"
			event.Event.Category = "tool"
			if event.Severity == "info" {
				event.Severity = "high"
			}
			event.Message = "Grok Bot MCP tool call failed"
		} else {
			event.Event.Action = "mcp.tool_invoked"
			event.Event.Category = "mcp"
			event.Message = "Grok Bot MCP tool call"
		}
		event.Event.Fidelity = observed
		if tool != "" || server != "" {
			event.MCP = &MCPInfo{Server: server, Tool: tool}
		}
		if tool != "" {
			event.Tool = &ToolInfo{Name: tool}
		}
	case cursorGrokBotBrowserNavigation:
		event.Event.Action = "tool.invoked"
		event.Event.Category = "tool"
		event.Event.Fidelity = observed
		event.Tool = &ToolInfo{Name: "browser_navigation"}
		event.Message = "Grok Bot browser navigation"
		// Cursor normalizes the URL to scheme://host/path before export. The host is the part a
		// detection can match on, and server.address is the schema field that already means "the
		// remote end of this action".
		if raw := FirstString(attrs, "cursor.grok_bot.browser.url"); raw != "" {
			event.Message = raw
			if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
				event.Server = &ServerInfo{Address: parsed.Hostname()}
			}
		}
	case cursorGrokBotComputerUseSession:
		event.Event.Action = "tool.invoked"
		event.Event.Category = "tool"
		event.Event.Fidelity = observed
		event.Tool = &ToolInfo{Name: "computer_use"}
		event.Message = "Grok Bot computer use session"
	case cursorAPIRequest:
		// Same classification Claude Code's api_request gets: a model call is session activity,
		// and the token counts on it are the only per-request usage this export carries.
		event.Event.Action = "session.activity"
		event.Event.Category = "session"
		event.Event.Fidelity = observed
		event.Message = "Grok Bot model request"
		for tokenType, key := range map[string]string{
			"input":          "cursor.api.request.input_tokens",
			"output":         "cursor.api.request.output_tokens",
			"cache_read":     "cursor.api.request.cache_read_tokens",
			"cache_creation": "cursor.api.request.cache_creation_tokens",
		} {
			if value, ok := Int64Attr(attrs, key); ok {
				ApplyTokenUsage(event, tokenType, value)
			}
		}
		if event.GenAI != nil {
			// ApplyTokenUsage records the last type it saw as gen_ai.token.type, which describes a
			// single-count datapoint and not a request carrying all four counts at once.
			event.GenAI.Token = nil
		}
	case cursorAPIError:
		event.Event.Action = "session.error"
		event.Event.Category = "session"
		event.Event.Fidelity = observed
		event.Message = "Grok Bot model request failed"
	}
}

// applyCursorGrokBotContext sets the fields every Grok Bot record shares, on the log and the
// metric path alike: the conversation id Cursor uses as the session key, a cloud origin (the
// shell normalizer overrides it for a command that ran on the member's machine), and the export's
// opaque team-scoped user id in place of the collector process user, which has nothing to do
// with whoever was driving the Bot.
func applyCursorGrokBotContext(event *Event, attrs map[string]interface{}) {
	if event == nil || !IsCursorGrokBotSurface(attrs) {
		return
	}
	if id := FirstString(attrs, "cursor.conversation.id"); id != "" {
		if event.Session == nil {
			event.Session = &SessionInfo{}
		}
		if event.Session.ID == "" {
			event.Session.ID = id
		}
	}
	if event.Origin == "" {
		event.Origin = asymptoteobserve.OriginCloud
	}
	event.User = UserInfo{UID: FirstString(attrs, "cursor.user.id")}
}
