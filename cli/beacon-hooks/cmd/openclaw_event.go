package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	hookdiff "github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/diff"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/spf13/cobra"
)

// openclaw-event is the single entry point for every OpenClaw Gateway plugin hook payload.
//
// OpenClaw is a self-hosted gateway that connects chat apps -- Discord, Slack, Signal, WhatsApp
// and the rest -- to coding agents. Its telemetry reaches Beacon over two paths that collect
// different things and are both wanted:
//
//   - The OTLP path (`beacon endpoint integrations openclaw`) configures OpenClaw's own
//     `diagnostics-otel` plugin. It carries gateway-level traces and metrics, and it carries
//     nothing about the agent's actions unless `captureContent` is turned on -- which OpenClaw
//     documents as off by default.
//   - This path, a Beacon-managed plugin, subscribes to OpenClaw's typed plugin hooks and reports
//     the agent's actual work: sessions, prompts, tool calls, commands, files, MCP activity and
//     token usage.
//
// The transport is the one every Beacon-managed plugin uses: the plugin spawns this binary and
// writes one JSON object to its stdin. The envelope the plugin writes is
//
//	{"hook": "before_tool_call", "sessionId": "...", "sessionKey": "...", "runId": "...",
//	 "agentId": "...", "cwd": "...", "model": "openai/gpt-5", "channel": "discord",
//	 "event": { ...the verbatim hook event... }}
//
// `event` stays nested rather than being spread onto the envelope, which is the one structural
// difference from the Pi-family and Cline plugins. OpenClaw hands a handler `(event, ctx)` as two
// separate objects whose field names overlap -- `runId`, `sessionKey` and `sessionId` are on both,
// and `before_tool_call` carries a `params` object whose keys are the model's, not OpenClaw's. A
// flattened envelope would let a tool argument named `sessionId` overwrite the session the event
// belongs to. Keeping the two apart costs one level of nesting and makes that impossible.
var openClawEventCmd = &cobra.Command{
	Use:   "openclaw-event",
	Short: "Record OpenClaw Gateway hook telemetry",
	Long:  `openclaw-event receives raw Beacon OpenClaw plugin payloads and writes local endpoint telemetry.`,
	Run:   runOpenClawEvent,
}

func init() {
	rootCmd.AddCommand(openClawEventCmd)
}

// openClawPlatform is the `--platform` value an OpenClaw install is recorded under.
//
// It selects the session-id and working-directory readers in helpers.go and is what
// asymptoteobserve.CollectionMethodForPlatform keys on to report this runtime as `plugin` rather
// than `hook`. The harness name it normalizes to is `openclaw_gateway`, shared with the OTLP path
// so one gateway's activity is one harness in every query that groups by harness.name.
const openClawPlatform = "openclaw"

// openClawToolNameSeparator is what OpenClaw puts between an MCP server's name and its tool's.
//
// `TOOL_NAME_SEPARATOR` in OpenClaw's agent-bundle-mcp-names.ts. See openClawMCPServerTool for why
// this separator is the only thing that identifies an MCP call on this runtime.
const openClawToolNameSeparator = "__"

func runOpenClawEvent(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, openClawPlatform)
	logger := newHookLogger("openclaw-event", openClawPlatform, sessionID)
	for _, event := range openClawEndpointEvents(input, sessionID) {
		if event.action == "" {
			continue
		}
		_ = logger.EndpointEvent(event.action, event.category, event.severity, event.message, event.fields)
	}
	outputJSON(emptyResponse)
}

// supportedOpenClawHooks lists every OpenClaw plugin hook this mapper handles.
//
// These strings are the contract between the managed plugin's registration list and the mapper: a
// typo on either side produces no telemetry rather than an error, so both sides pin the list and a
// test asserts each entry still maps to an event.
//
// OpenClaw publishes forty-one typed hooks. The ten below are the ones that describe what the
// agent did; the rest describe how the gateway got its work done, and the notable exclusions are
// worth stating because each looks collectible until you read its payload:
//
//   - `llm_input` carries the full system prompt, the assembled prompt and the entire history
//     array on every model call. `message_received` already carries the operator's actual message,
//     which is the prompt an investigation asks about, at a fraction of the volume.
//   - `agent_end` carries the whole `messages` array for the turn. Its unique fields are a success
//     flag and a duration, which do not justify writing the conversation to the log a second time.
//   - `model_call_started` / `model_call_ended` fire once per provider call, so a single turn with
//     a tool loop produces a handful of pairs, and neither carries usage. `llm_output` reports the
//     turn's usage once.
//   - `tool_result_persist` and `before_message_write` are OpenClaw's two *synchronous* hooks:
//     promises are ignored with a warning, so a handler that spawns this binary could not wait for
//     it. Both also duplicate tool and message content already collected here.
//   - `before_prompt_build`, `agent_turn_prepare` and `heartbeat_prompt_contribution` are prompt
//     *injection* hooks. Beacon observes; registering them would ask OpenClaw for permission to
//     rewrite a prompt, which Beacon must never hold.
//   - The channel-delivery hooks (`message_sending`, `message_sent`, `reply_payload_sending`,
//     `reply_dispatch`, `before_dispatch`, `inbound_claim`) describe chat plumbing rather than
//     agent action, and the claim-kind ones can swallow a message outright.
//   - `skill_proposal_evaluate` is an Evaluate hook: registering it makes the plugin a voting
//     evaluator on whether a skill may be installed. That is enforcement, not observation.
func supportedOpenClawHooks() []string {
	return []string{
		"session_start",
		"session_end",
		"message_received",
		"before_tool_call",
		"after_tool_call",
		"llm_output",
		"before_compaction",
		"after_compaction",
		"subagent_spawned",
		"subagent_ended",
	}
}

// openClawEndpointEvents maps one OpenClaw envelope onto the endpoint events it justifies.
//
// An unrecognized hook returns nothing rather than a generic event, matching the Cline and
// Pi-family mappers: OpenClaw publishes four times as many hooks as the plugin registers, and one
// arriving here should be silent rather than becoming an undifferentiated "something happened" row
// that every query matches and none can explain.
func openClawEndpointEvents(input map[string]interface{}, sessionID string) []normalizedEvent {
	event := firstMap(input, "event")
	if event == nil {
		event = map[string]interface{}{}
	}
	fields := openClawBaseFields(input, event, sessionID)

	switch getFirstStr(input, "hook") {
	case "session_start":
		// A resumed session has history behind it, which a reader counting sessions needs to know.
		if resumed := getFirstStr(event, "resumedFrom"); resumed != "" {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"openclaw_resumed_from": resumed})
		}
		return openClawOne("session.started", "session", "info", "session started", fields)

	case "session_end":
		// OpenClaw names nine reasons a session ends, and they are not equivalent: `shutdown` and
		// `restart` are the gateway stopping, `compaction` is the session rotating rather than
		// finishing, and `deleted` is an operator removing it. Recorded verbatim rather than
		// collapsed, because a session that ended by deletion and one that idled out are different
		// facts about the same id.
		extra := map[string]interface{}{}
		if reason := getFirstStr(event, "reason"); reason != "" {
			extra["openclaw_session_end_reason"] = reason
		}
		// The session id that continues this conversation, when a rotation produced one. Without
		// it a compaction looks like a session ending and an unrelated one starting.
		if next := getFirstStr(event, "nextSessionId"); next != "" {
			extra["openclaw_next_session_id"] = next
		}
		if count, ok := firstToolIntAcross([]map[string]interface{}{event}, "messageCount"); ok {
			extra["openclaw_message_count"] = count
		}
		if len(extra) > 0 {
			fields["raw"] = mergeNested(fields["raw"], extra)
		}
		return openClawOne("session.ended", "session", "info", "session ended", fields)

	case "message_received":
		return openClawPromptEvents(input, event, fields)

	case "before_tool_call":
		// The pre-execution half of a tool call: OpenClaw has resolved the tool and its arguments
		// but nothing has run yet. Recorded as tool.invoked, and deliberately not as an approval --
		// see the approvals note below.
		mergeMap(fields, openClawToolFields(event, nil))
		applyToolCallID(fields, event)
		return openClawOne("tool.invoked", "tool", "info", "tool invoked", fields)

	case "after_tool_call":
		return openClawAfterToolEvents(event, fields)

	case "llm_output":
		return openClawLlmOutputEvents(event, fields)

	case "before_compaction":
		if count, ok := firstToolIntAcross([]map[string]interface{}{event}, "messageCount"); ok {
			fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"openclaw_message_count": count})
		}
		return openClawOne("session.compacting", "session", "info", "session compacting", fields)

	case "after_compaction":
		extra := map[string]interface{}{}
		if count, ok := firstToolIntAcross([]map[string]interface{}{event}, "compactedCount"); ok {
			extra["openclaw_compacted_count"] = count
		}
		// A compaction can rotate the physical session. The previous id is the only link back to
		// the events written before the rotation.
		if previous := getFirstStr(event, "previousSessionId"); previous != "" {
			extra["openclaw_previous_session_id"] = previous
		}
		if len(extra) > 0 {
			fields["raw"] = mergeNested(fields["raw"], extra)
		}
		return openClawOne("session.compacted", "session", "info", "session compacted", fields)

	case "subagent_spawned":
		extra := map[string]interface{}{}
		if child := getFirstStr(event, "childSessionKey"); child != "" {
			extra["openclaw_child_session_key"] = child
		}
		if mode := getFirstStr(event, "mode"); mode != "" {
			extra["openclaw_subagent_mode"] = mode
		}
		if len(extra) > 0 {
			fields["raw"] = mergeNested(fields["raw"], extra)
		}
		// The child's model rather than the parent's, when OpenClaw resolved a different one: a
		// subagent routed to a cheaper or less restricted model is exactly what the row is for.
		if model := getFirstStr(event, "resolvedModel"); model != "" {
			fields["model"] = model
		}
		return openClawOne("subagent.started", "session", "info", "subagent started", fields)

	case "subagent_ended":
		extra := map[string]interface{}{}
		if target := getFirstStr(event, "targetSessionKey"); target != "" {
			extra["openclaw_child_session_key"] = target
		}
		if outcome := getFirstStr(event, "outcome"); outcome != "" {
			extra["openclaw_subagent_outcome"] = outcome
		}
		if reason := getFirstStr(event, "reason"); reason != "" {
			extra["openclaw_subagent_reason"] = reason
		}
		if len(extra) > 0 {
			fields["raw"] = mergeNested(fields["raw"], extra)
		}
		if failure := getFirstStr(event, "error"); failure != "" {
			fields["error"] = map[string]interface{}{"type": "subagent_error", "message": failure}
			return openClawOne("subagent.stopped", "session", "high", "subagent failed", fields)
		}
		return openClawOne("subagent.stopped", "session", "info", "subagent stopped", fields)

	default:
		return nil
	}
}

// Approvals: why this mapper writes none, stated where a reader will look for them.
//
// OpenClaw does ask operators to approve tool calls, and it exposes no hook that reports the
// answer. Its forty-one typed hooks contain no `tool_approval_*` event; the only approval surface a
// plugin has is `before_tool_call`'s `requireApproval` result, which is a plugin *requesting* a
// prompt and being told its own outcome. Beacon does not request approvals, so it never sees one.
//
// There is a second thing on this runtime that looks like an approval and is not. An `exec` tool
// result carries `details.approvalReviewOutcome` with values `approved`, `denied` and `reviewing`,
// alongside `details.approvalReviews[]` entries that have a status and a rationale. OpenClaw's own
// type calls these "model-backed approval reviews": they are an LLM judging the command, not a
// person deciding about it. Recording one as `approval.allowed` would put a machine's opinion into
// the field every approval detection reads as a human decision -- the single worst failure mode
// this vocabulary has. They stay in `raw` with the rest of the tool result, where they are still
// searchable and cannot be mistaken for consent.
//
// So approvals are not synthesized here, matching the Cline, Pi, goose, OpenHands and Kiro
// decision. The endpoint policy seam is the supported way to gate an OpenClaw tool call, and it
// records its own decisions with `policy.enforcement` rather than borrowing this vocabulary.

// openClawOne wraps a single event, prefixing the runtime's name onto the message.
func openClawOne(action, category, severity, messageSuffix string, values map[string]interface{}) []normalizedEvent {
	return []normalizedEvent{{
		action:   action,
		category: category,
		severity: severity,
		message:  "OpenClaw " + messageSuffix,
		fields:   values,
	}}
}

// openClawBaseFields builds the identity every OpenClaw event carries.
//
// The whole envelope goes under `raw.openclaw`, which is what preserves the fields OpenClaw
// reports that the endpoint schema has no column for -- the channel a turn arrived on, the agent
// id inside a multi-agent gateway, the requester's sender id. It is sanitized, secret-redacted and
// string-limited like every other field, and it is the first thing dropped when an event exceeds
// the size ceiling.
func openClawBaseFields(input, event map[string]interface{}, sessionID string) map[string]interface{} {
	fields := sessionFieldsForPlatform(sessionID, input, openClawPlatform)
	applyWorkspaceFieldsForPlatform(fields, input, "", openClawPlatform)
	fields["raw"] = map[string]interface{}{openClawPlatform: input}

	// The model the turn ran on. `resolvedRef` is preferred over the context's because it keeps
	// the provider prefix ("openai/gpt-5" rather than "gpt-5"), which the writer then splits into
	// the canonical model name and `gen_ai.provider.name`. Handing it the bare id would leave the
	// provider unrecorded, and a gateway routes one model through several of them.
	if model := getFirstStr(event, "resolvedRef"); model != "" {
		fields["model"] = model
	} else if model := getFirstStr(input, "model"); model != "" {
		fields["model"] = model
	}

	// OpenClaw's own identifiers for a turn and a conversation. `runId` is one end-to-end agent
	// turn and is stable across every model call, retry and reply chunk inside it; `sessionKey` is
	// the canonical conversation key, which survives the session-id rotation a compaction causes.
	// Neither has an endpoint schema field, and both are how an operator correlates a Beacon row
	// with what OpenClaw's own logs and dashboard show.
	extra := map[string]interface{}{}
	for key, value := range map[string]string{
		"openclaw_run_id":      getFirstStr(input, "runId"),
		"openclaw_session_key": getFirstStr(input, "sessionKey"),
		"openclaw_agent_id":    getFirstStr(input, "agentId"),
		"openclaw_channel":     getFirstStr(input, "channel"),
	} {
		if value != "" {
			extra[key] = value
		}
	}
	if len(extra) > 0 {
		fields["raw"] = mergeNested(fields["raw"], extra)
	}
	return fields
}

// openClawPromptEvents records the inbound message that started a turn.
//
// `message_received` rather than `llm_input` is the prompt on this runtime, and the choice is not
// arbitrary. OpenClaw is a gateway: what a person actually wrote is the chat message that arrived
// on Discord or Signal, while `llm_input`'s `prompt` is that message after channel context,
// history and system material have been assembled around it. The first is evidence of what someone
// asked for; the second is an artifact of how OpenClaw asked the model. `llm_input` also costs the
// entire history array on every model call, and `message_received` is not behind the
// `allowConversationAccess` config gate that every conversation hook is.
func openClawPromptEvents(input, event, fields map[string]interface{}) []normalizedEvent {
	prompt := getFirstStr(event, "content", "body")
	if prompt == "" {
		return nil
	}
	fields["prompt"] = map[string]interface{}{"text": prompt}
	fields["gen_ai"] = mergeNested(fields["gen_ai"], map[string]interface{}{
		"input": map[string]interface{}{"messages": asymptoteobserve.TextInputMessages(prompt)},
	})
	fields["content"] = retainedContentFields(prompt)

	// Who sent it, and over which chat app. On a single-user coding CLI this is implied; on a
	// gateway that fans several people's chat accounts into one agent it is the difference between
	// "this machine ran a command" and "this person asked it to".
	extra := map[string]interface{}{}
	if from := getFirstStr(event, "from", "senderId"); from != "" {
		extra["openclaw_sender"] = from
	}
	if channel := getFirstStr(input, "channel"); channel != "" {
		extra["openclaw_channel"] = channel
	}
	if len(extra) > 0 {
		fields["raw"] = mergeNested(fields["raw"], extra)
	}
	events := []normalizedEvent{{
		action: "prompt.submitted", category: "prompt", severity: "info",
		message: "Prompt submitted to OpenClaw", fields: fields,
	}}
	if link, ok := handoffLinkEvent(fields, prompt); ok {
		events = append(events, link)
	}
	return events
}

// openClawLlmOutputEvents records what a finished model response reports: its token usage, and the
// assistant's answer.
//
// This is the only OpenClaw hook that carries usage, and it is behind the
// `plugins.entries.<id>.hooks.allowConversationAccess` config gate -- non-bundled plugins get no
// conversation hooks without it, so an install that skips that key collects everything here except
// tokens and the agent's reply. The installer writes it; `beacon endpoint hooks status` is where a
// missing one shows up.
func openClawLlmOutputEvents(event, fields map[string]interface{}) []normalizedEvent {
	var events []normalizedEvent

	if text := openClawAssistantText(event); text != "" {
		messageFields := cloneFields(fields)
		messageFields["gen_ai"] = mergeNested(messageFields["gen_ai"], map[string]interface{}{
			"output": map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{
					"role":  "assistant",
					"parts": []interface{}{map[string]interface{}{"type": "text", "content": text}},
				}},
			},
		})
		messageFields["content"] = retainedContentFields(text)
		events = append(events, openClawOne("agent.message", "agent", "info", "agent message", messageFields)...)
	}

	if usage := openClawUsage(firstMap(event, "usage")); len(usage) > 0 {
		usageFields := cloneFields(fields)
		usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{"usage": usage})
		// The context window this turn was measured against. Kept separate from usage for the
		// reason applyContextSize states: usage is additive and every report sums it, while a
		// context budget is a level at one moment and summing it is meaningless.
		if limit, ok := firstToolIntAcross([]map[string]interface{}{event}, "contextTokenBudget"); ok && limit > 0 {
			if used, ok := usage["input_tokens"].(int); ok && used > 0 {
				usageFields["gen_ai"] = mergeNested(usageFields["gen_ai"], map[string]interface{}{
					"context": map[string]interface{}{"used_tokens": used, "limit_tokens": limit},
				})
			}
		}
		events = append(events, openClawOne("token.usage", "metric", "info", "token usage", usageFields)...)
	}

	return events
}

// openClawAssistantText joins the assistant's visible answer for one model response.
//
// `assistantTexts` is a string array because one response can be split across several text parts.
// Only that field is read: `lastAssistant` is the provider's own message object, whose shape
// differs per provider, and reading it would make Beacon's output depend on which model the
// gateway happened to route to.
func openClawAssistantText(event map[string]interface{}) string {
	items, ok := event["assistantTexts"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// openClawUsage normalizes OpenClaw's usage object into gen_ai.usage.
//
// OpenClaw names its fields input/output/cacheRead/cacheWrite/total, none of which match the OTel
// GenAI semconv names Beacon writes, and two of which are nested objects on Beacon's side rather
// than scalars. The mapping is spelled out against the canonical shape rather than copied through,
// so gen_ai.usage stays the only token representation in the log.
//
// `total` is deliberately dropped. Beacon's usage shape has no total, and a redundant field that
// can disagree with its own parts is worse than an absent one.
//
// No cost. OpenClaw computes a per-turn USD figure (`turnUsd` on its reply usage state) from an
// operator-configured cost table rather than from a provider report, and it is not on this hook.
// Beacon records runtime-*reported* cost only, so a locally estimated one stays out of
// gen_ai.usage.cost_usd even where it is available.
func openClawUsage(usage map[string]interface{}) map[string]interface{} {
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
		out["cache_creation"] = map[string]interface{}{"input_tokens": value}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// openClawAfterToolEvents maps a completed tool call onto the outcome events it justifies.
//
// One call can become several events: an `apply_patch` that touched three files is three
// `file.modified` rows, because a single row naming one of the three would under-report the change
// and a row naming none would be unsearchable. That is the OpenHands shape and it is chosen for
// the same reason.
func openClawAfterToolEvents(event, fields map[string]interface{}) []normalizedEvent {
	name := openClawToolName(event)
	result := firstMap(event, "result")
	mergeMap(fields, openClawToolFields(event, result))
	applyToolCallID(fields, event)

	// OpenClaw reports a failed tool call on the same hook as a successful one, distinguished by
	// an `error` string. The write that did not land must not be recorded as a write that did.
	if failure := getFirstStr(event, "error"); failure != "" {
		fields["error"] = map[string]interface{}{"type": "tool_error", "message": failure}
		return openClawOne("tool.failed", "tool", "high", "tool failed", fields)
	}

	if duration, ok := firstToolIntAcross([]map[string]interface{}{event}, "durationMs"); ok {
		fields["raw"] = mergeNested(fields["raw"], map[string]interface{}{"openclaw_duration_ms": duration})
	}

	if paths := openClawPatchPaths(event); len(paths) > 0 {
		return openClawPatchEvents(event, fields, paths)
	}

	action, category := openClawToolAction(name)
	// A file action with no file is not a file action, and a command action with no command is not
	// a command. A custom tool registered by a plugin can share a built-in's name, and a built-in
	// can be called with arguments that failed to resolve, so reporting either would produce a row
	// every scoped query matches and none can explain -- the guard the Cline and Pi-family mappers
	// apply for the same reason.
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
	return openClawOne(action, category, openClawSeverity(action, fields), openClawToolMessageSuffix(action), fields)
}

// openClawPatchEvents turns one apply_patch call into one file event per path it changed.
func openClawPatchEvents(event, fields map[string]interface{}, paths []string) []normalizedEvent {
	// openClawToolFields retained the patch on the shared fields, which every per-path event is
	// cloned from. Lifted off before the clones are taken so it lands on exactly one of them --
	// see the comment below the loop for why one and not all.
	patchContent := fields["content"]
	delete(fields, "content")

	var events []normalizedEvent
	for _, path := range paths {
		pathFields := cloneFields(fields)
		file := map[string]interface{}{"path": path, "operation": "modify"}
		if language := strings.TrimPrefix(filepath.Ext(path), "."); language != "" {
			file["language"] = language
		}
		pathFields["file"] = file
		pathFields["tool"] = mergeNested(pathFields["tool"], map[string]interface{}{"path": path})
		events = append(events, openClawOne("file.modified", "file", "info", "file modified", pathFields)...)
	}
	// The patch envelope itself, retained once on the first event rather than repeated on each.
	// It is the only record of what actually changed -- OpenClaw's apply_patch result carries a
	// summary, not the content -- and copying it per path would multiply the largest field in the
	// event by the number of files the patch touched.
	if len(events) > 0 && patchContent != nil {
		events[0].fields["content"] = patchContent
	}
	return events
}

// openClawPatchPaths returns the files an apply_patch call changed.
//
// OpenClaw derives these itself and hands them to `before_tool_call` as `derivedPaths`, which its
// own documentation calls a lenient convenience hint rather than an authoritative parse. That is
// the right level of trust for telemetry and the wrong one for a policy decision: a path Beacon
// records that the patch did not touch is a misleading row, while a path a policy engine misses is
// an unenforced rule. Beacon only records, so the hint is used as given.
//
// `after_tool_call` does not carry `derivedPaths`, so the plugin copies the value forward from the
// `before_tool_call` it saw for the same `toolCallId`. When it has none -- an OpenClaw build that
// does not derive paths for this envelope, or a call the plugin never saw proposed -- this returns
// nothing and the call is recorded as a completed tool with its patch text rather than as a file
// change with an invented path.
func openClawPatchPaths(event map[string]interface{}) []string {
	if !strings.EqualFold(openClawToolName(event), "apply_patch") {
		return nil
	}
	items, ok := event["derivedPaths"].([]interface{})
	if !ok {
		return nil
	}
	var paths []string
	for _, item := range items {
		if path, ok := item.(string); ok && strings.TrimSpace(path) != "" {
			paths = append(paths, hookdiff.NormalizePath(path))
		}
	}
	return paths
}

// openClawToolName reads the tool name off a before_tool_call or after_tool_call event.
func openClawToolName(event map[string]interface{}) string {
	return getFirstStr(event, "toolName", "tool_name")
}

// openClawToolFields builds the tool, command, file and MCP blocks for one OpenClaw tool event.
//
// OpenClaw's built-in tools have fixed, documented argument shapes, so these are read by name
// rather than guessed at across spellings. A tool registered by another plugin carries an
// arbitrary shape and gets `tool.name` plus its raw arguments without a command or file block
// invented for it.
func openClawToolFields(event map[string]interface{}, result map[string]interface{}) map[string]interface{} {
	name := openClawToolName(event)
	params := firstMap(event, "params")
	if params == nil {
		params = map[string]interface{}{}
	}
	details := firstMap(result, "details")

	fields := map[string]interface{}{}
	tool := map[string]interface{}{}
	if name != "" {
		tool["name"] = name
	}

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "exec":
		if command := getFirstStr(params, "command"); command != "" {
			tool["command"] = command
			commandFields := map[string]interface{}{"command": command}
			// The exit code and the captured output live on the result's details, and only for a
			// call that finished: an exec that is still running reports `status: "running"` with a
			// tail instead. Read by status rather than by presence so a backgrounded command's
			// partial tail is never recorded as its final output.
			switch getFirstStr(details, "status") {
			case "completed", "failed":
				if code, ok := firstToolIntAcross([]map[string]interface{}{details}, "exitCode"); ok {
					commandFields["exit_code"] = code
				}
				if output := getFirstStr(details, "aggregated"); output != "" {
					commandFields["output"] = output
					fields["content"] = retainedContentFields(output)
				}
			}
			if _, ok := fields["content"]; !ok {
				fields["content"] = retainedContentFields(command)
			}
			fields["command"] = commandFields
		}
	case "read", "write", "edit":
		if path := getFirstStr(params, "path", "file_path", "filePath"); path != "" {
			path = hookdiff.NormalizePath(path)
			tool["path"] = path
			file := map[string]interface{}{"path": path, "operation": openClawFileOperation(name)}
			if language := strings.TrimPrefix(filepath.Ext(path), "."); language != "" {
				file["language"] = language
			}
			if diff := openClawFileDiff(name, path, params); diff != "" {
				// The diff text itself, alongside the hash and byte count taken before the writer
				// redacts or truncates the stored copy. The `content` marker beside it says
				// whether what landed in the log is the whole diff; the hash and size say what the
				// original was. diffFields builds the same three keys for the shared hook path, so
				// a Beacon consumer reads one shape whichever runtime produced the row.
				sum := sha256.Sum256([]byte(diff))
				file["diff"] = diff
				file["diff_hash"] = hex.EncodeToString(sum[:])
				file["diff_bytes"] = len(diff)
				fields["content"] = retainedContentFields(diff)
			}
			fields["file"] = file
		}
	case "apply_patch":
		// The patch envelope is the whole argument. Paths come from derivedPaths in
		// openClawPatchPaths; here it only supplies the tool block and the retained content for a
		// call whose paths could not be derived.
		if patch := getFirstStr(params, "input"); patch != "" {
			fields["content"] = retainedContentFields(patch)
		}
	}

	if len(tool) > 0 {
		fields["tool"] = tool
	}
	openClawApplyMCPAttribution(fields, name)
	return fields
}

// openClawFileDiff builds the diff for an OpenClaw write or edit.
//
// OpenClaw's `edit` takes `oldText` and `newText`, and requires `oldText` to match the file exactly
// and uniquely -- so the pair describes a fragment, not the file, which is the shape
// FromEditFragments exists for. Its `write` takes the whole `content` with no prior version
// available on the hook, so the diff is the file's new content against nothing: a creation.
//
// A read has no diff, and neither does an edit whose two sides are identical; both return "" and
// the event carries its path without one.
func openClawFileDiff(name, path string, params map[string]interface{}) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit":
		return hookdiff.FromEditFragments(path, getFirstStr(params, "oldText", "old_string", "oldString"), getFirstStr(params, "newText", "new_string", "newString"))
	case "write":
		return hookdiff.FromContentChange(path, "", getFirstStr(params, "content"))
	default:
		return ""
	}
}

// openClawFileOperation maps an OpenClaw file tool onto the schema's operation vocabulary.
func openClawFileOperation(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "read"
	case "write":
		return "create"
	default:
		return "modify"
	}
}

// openClawToolAction maps an OpenClaw tool name onto the endpoint action its completion represents.
func openClawToolAction(name string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "exec":
		return "command.executed", "command"
	case "read":
		return "file.read", "file"
	case "write":
		return "file.created", "file"
	case "edit", "apply_patch":
		return "file.modified", "file"
	default:
		if server, tool := openClawMCPServerTool(name); server != "" || tool != "" {
			return "mcp.tool_invoked", "mcp"
		}
		// `ls`, `process`, the openclaw-family tools, and anything a plugin registered. Real tool
		// activity with no file or command semantics worth asserting.
		return "tool.completed", "tool"
	}
}

// openClawSeverity raises a command event that reported a non-zero exit.
//
// Only commands, and only on a code the runtime actually reported: a tool whose result Beacon
// could not read is not evidence of failure, and marking it as such would put noise where an
// operator triages.
func openClawSeverity(action string, fields map[string]interface{}) string {
	if action != "command.executed" {
		return "info"
	}
	command, ok := fields["command"].(map[string]interface{})
	if !ok {
		return "info"
	}
	if code, ok := command["exit_code"].(int); ok && code != 0 {
		return "medium"
	}
	return "info"
}

// openClawToolMessageSuffix returns the human-readable half of a tool event's message.
func openClawToolMessageSuffix(action string) string {
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

// openClawApplyMCPAttribution fills the `mcp` block when a tool name names an MCP-routed tool.
func openClawApplyMCPAttribution(fields map[string]interface{}, toolName string) {
	server, tool := openClawMCPServerTool(toolName)
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

// openClawMCPServerTool splits an OpenClaw MCP tool name into its server and tool halves.
//
// OpenClaw names a tool from an MCP server `<safeServerName>__<toolName>` -- two segments, with no
// `mcp` anywhere in the name, no `mcp_*` argument, and nothing in the result that says where the
// tool came from. So the shared `mcp__server__tool` reader finds nothing, and a GitHub server's
// `github__create_issue` would otherwise be classified by tool name alone and land under
// tool.completed with no server attribution at all. That is the goose situation and it has the
// same single available signal: the separator.
//
// The `mcp__` form is tried first anyway, because OpenClaw's own Codex and Claude harness bridges
// mint Claude-style names (`mcp__openclaw__automations`) that reach a hook through the same field,
// and a three-segment name split on the first separator would report the server as `mcp`.
//
// Two segments are required and both must be non-empty, so a built-in tool with no separator, and
// a name that merely ends in one, are left alone. A built-in whose own name contained `__` would be
// misread here -- none does, in OpenClaw's factory descriptor list or its tool catalog.
func openClawMCPServerTool(toolName string) (string, string) {
	if server, tool := deriveMCPServerTool(toolName); server != "" || tool != "" {
		return server, tool
	}
	trimmed := strings.TrimSpace(toolName)
	server, tool, ok := strings.Cut(trimmed, openClawToolNameSeparator)
	if !ok || server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}
