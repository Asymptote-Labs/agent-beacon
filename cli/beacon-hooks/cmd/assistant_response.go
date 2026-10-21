package cmd

import (
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// lastAssistantMessagePlatforms names the runtimes whose Stop payload documents the agent's final
// reply as `last_assistant_message`.
//
// Claude Code added the field so a hook would not have to read the transcript, which is written
// asynchronously and can be missing the turn's last message when Stop fires; reading it here is
// race-free where the transcript is not. A list rather than every Claude-shaped runtime: Devin CLI
// and Factory Droid send Claude-shaped Stop payloads but document no such field, and a key read
// for a runtime that never wrote it is a guess about what it would mean there.
var lastAssistantMessagePlatforms = map[string]bool{
	"claude": true,
	// Autopilot documents the field on Stop under the same name, as the text of the turn's final
	// reply when it ended on one.
	autopilotPlatform: true,
}

// stopAssistantResponse is the agent's final reply as this platform's Stop payload reports it, or
// "" when the payload carries none: a turn that ended on a tool call, an interrupt, or a runtime
// that reports no reply at all.
func stopAssistantResponse(platform string, input map[string]interface{}) string {
	switch {
	case platform == kiroPlatform:
		return kiroAssistantResponse(input)
	case lastAssistantMessagePlatforms[platform]:
		return getFirstStr(input, "last_assistant_message")
	default:
		return ""
	}
}

// emitStopAssistantResponse records the reply a Stop payload carries, if any.
//
// The shared stop command records that a turn finished. Without this, what the agent said at the
// end of it -- the half of a session prompt.submitted does not cover -- reached the log only when
// someone later ran the runtime's session sync, and never on a runtime without one.
func emitStopAssistantResponse(logger *logging.Logger, input map[string]interface{}, sessionID string) {
	emitAssistantMessage(logger, input, sessionFields(sessionID, input), stopAssistantResponse(platformFlag, input))
}

// emitAssistantMessage writes one `agent.message` event carrying text as the assistant's reply.
//
// Every hook-side capture of a reply goes through here so they share one shape: the text in the
// OTel GenAI output-messages form with the retention marker beside it, exactly as agent-thought
// writes reasoning, under the action the session-store mappers already use for the same text. A
// reader that understands one capture path understands all of them. Empty or blank text writes
// nothing: an empty agent.message would assert the agent answered with nothing, which is different
// from the runtime not reporting an answer.
func emitAssistantMessage(logger *logging.Logger, input, fields map[string]interface{}, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if fields == nil {
		fields = map[string]interface{}{}
	}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"output": map[string]interface{}{"messages": asymptoteobserve.TextOutputMessages(text)},
	})
	fields["content"] = retainedContentFields(text)
	emitHookEvent(logger, "agent.message", "session", "info", "Agent response captured", input, fields)
}
