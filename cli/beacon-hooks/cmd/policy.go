package cmd

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/policy"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// policyCandidate describes the imminent tool call. The same fields feed both the
// provider request event and, on a deny, the denial telemetry event.
type policyCandidate struct {
	action   string
	category string
	fields   map[string]interface{}
}

// enforcePolicy consults the configured policy provider for the imminent tool
// call. If the provider denies and the current platform has a deny response
// shape, it records denial telemetry and returns that response plus true.
// Otherwise it returns nil, false and the caller proceeds with the normal allow
// flow. It is a no-op (nil, false) when no provider is configured.
func enforcePolicy(logger *logging.Logger, input map[string]interface{}, sessionID string, phase policycontract.Phase) (map[string]interface{}, bool) {
	if !policy.Enabled() {
		return nil, false
	}
	candidate := newPolicyCandidate(input, sessionID)
	resp := policy.Evaluate(context.Background(), policy.Request{
		Phase:    phase,
		Platform: platformFlag,
		Event:    candidate.event(),
	})
	if !resp.Denied() {
		return nil, false
	}
	reason := strings.TrimSpace(resp.Reason)
	if reason == "" {
		reason = "Tool call denied by policy provider"
	}
	deny := policyDenyResponse(reason, phase)
	if deny == nil {
		// Platform has no deny shape: honor "unknown platform -> allow".
		return nil, false
	}
	emitPolicyDenied(logger, input, candidate, resp, reason)
	return deny, true
}

// newPolicyCandidate builds the candidate from hook input the same way the
// telemetry path does (session + tool fields), plus a top-level command fallback
// for runtimes (e.g. Cursor shell hooks) that carry the command outside tool input.
func newPolicyCandidate(input map[string]interface{}, sessionID string) policyCandidate {
	toolName := getFirstStr(input, "tool_name", "toolName")
	if platformFlag == "antigravity" {
		toolName = antigravityToolName(input)
	}
	toolInput := resolveToolInput(input)
	hookEvent := getFirstStr(input, "hook_event_name", "hookEventName")

	fields := sessionFields(sessionID, input)
	for key, value := range toolFields(toolName, toolInput) {
		fields[key] = value
	}
	if _, ok := fields["command"]; !ok {
		if command := getFirstStr(input, "command"); command != "" {
			fields["command"] = map[string]interface{}{"command": command}
		}
	}

	action := actionForTool(hookEvent, toolName)
	// A command-bearing call with no more specific action is a command execution.
	if action == "tool.invoked" {
		if _, ok := fields["command"]; ok {
			action = "command.executed"
		}
	}
	return policyCandidate{action: action, category: categoryForAction(action), fields: fields}
}

func categoryForAction(action string) string {
	switch {
	case strings.HasPrefix(action, "command."):
		return "command"
	case strings.HasPrefix(action, "file."):
		return "file"
	case strings.HasPrefix(action, "mcp."):
		return "mcp"
	case strings.HasPrefix(action, "approval.") || strings.HasPrefix(action, "policy."):
		return "approval"
	default:
		return "tool"
	}
}

// event builds the asymptoteobserve.Event sent to the provider via a JSON
// round-trip of the same field map the telemetry writer uses, so the provider
// sees the same shape it would match in the runtime JSONL.
func (c policyCandidate) event() asymptoteobserve.Event {
	envelope := map[string]interface{}{
		"vendor":         "beacon",
		"product":        "endpoint-agent",
		"schema_version": "1.0",
		"event": map[string]interface{}{
			"kind":     "agent_runtime",
			"action":   c.action,
			"category": c.category,
		},
		"harness": map[string]interface{}{
			"name": platformFlag,
			// The contract promises the provider the same field names it would match in the
			// runtime JSONL, so the provenance marker belongs here too. No fidelity counterpart:
			// this event describes a tool call that has not happened yet, so there is no
			// observation for the action to be faithful to.
			"collection_method": asymptoteobserve.CollectionMethodForPlatform(platformFlag),
		},
	}
	for key, value := range c.fields {
		envelope[key] = value
	}
	var ev asymptoteobserve.Event
	data, err := json.Marshal(envelope)
	if err != nil {
		return ev
	}
	_ = json.Unmarshal(data, &ev)
	return ev
}

// emitPolicyDenied records the denial as endpoint telemetry, carrying
// policy.enforcement=enforce / policy.decision=deny and the matching approval
// fields.
func emitPolicyDenied(logger *logging.Logger, input map[string]interface{}, c policyCandidate, resp policycontract.Response, reason string) {
	fields := c.fields
	fields["approval"] = map[string]interface{}{
		"required": true,
		"decision": "deny",
		"reason":   reason,
	}
	policyField := map[string]interface{}{
		"enforcement": "enforce",
		"decision":    "deny",
		"reason":      reason,
	}
	if resp.RuleID != "" {
		policyField["id"] = resp.RuleID
	}
	fields["policy"] = policyField

	severity := strings.TrimSpace(resp.Severity)
	if severity == "" {
		severity = "high"
	}
	emitHookEvent(logger, "approval.denied", "approval", severity, reason, input, fields)
}

// policyDenyResponse returns the runtime-specific hook response that denies the
// tool call. A nil return means the platform has no confirmed deny shape, so the
// caller allows (unknown platform -> allow). It is phase-independent: a platform
// with a confirmed deny shape honors a deny in every phase the seam runs in, so a
// provider deny is never silently dropped for a platform we enforce on.
func policyDenyResponse(reason string, phase policycontract.Phase) map[string]interface{} {
	switch {
	case platformFlag == "cursor":
		return map[string]interface{}{"permission": "deny"}
	case isDevinLikePlatform(platformFlag):
		return map[string]interface{}{"decision": "reject"}
	case platformFlag == "antigravity" || platformFlag == "grok":
		return map[string]interface{}{"decision": "deny"}
	// Qwen Code shares Claude Code's deny shape exactly: `hookSpecificOutput.permissionDecision`
	// with a `permissionDecisionReason`, both required by its PreToolUse contract. The
	// `hookEventName` stays "PreToolUse" in both phases the seam runs in, matching the existing
	// claude behavior -- Qwen's PermissionRequest event reads a differently shaped
	// `hookSpecificOutput.decision` object, so a deny raised from that phase is not honored there.
	// Stated rather than hidden: the seam fails open, so the cost is a deny that does not take
	// effect on one of two phases, never a tool call blocked for the wrong reason.
	case platformFlag == "claude" || platformFlag == "qwen":
		return map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		}
	// Muse Code's deny contract is split between what has been measured and what has only been
	// read out of the binary, and this returns the half that has not been measured -- stated here
	// rather than left for a reader to discover.
	//
	// Measured, on a live hook: the host is fail-open on everything except `{"decision":"block"}`
	// and exit code 2. Every other shape, this one included, was ignored and the turn proceeded.
	// But that measurement was taken on UserPromptSubmit, whose deny cancels the whole turn --
	// which is not what a per-call policy deny means, and emitting it from a tool phase would
	// escalate "do not run this command" into "abandon what the user asked for". So the turn-family
	// shape is deliberately not used here.
	//
	// The tool-family shape below comes from the binary's own validation strings, which require
	// hookSpecificOutput to carry a hookEventName matching the firing event alongside
	// permissionDecision / permissionDecisionReason. Strong evidence, not measurement: the account
	// used for the measurement hit a billing error before any tool call, so no tool-side deny was
	// ever exercised.
	//
	// The cost of that being wrong is bounded and is the direction to be wrong in. Sending the
	// wrong family's shape was measured to be ignored silently, so an incorrect guess degrades to
	// exactly what returning nil does -- the deny does not take effect and the tool runs -- rather
	// than blocking a call for the wrong reason. Returning nil would give up the case where the
	// inference is right for no safety gain.
	//
	// hookEventName is derived from the phase rather than hardcoded, unlike the claude/qwen branch
	// above, because Muse requires it to match the firing event and Beacon binds each phase to a
	// distinct Muse event in the hooks file it writes. That is what lets a deny raised from the
	// PermissionRequest phase be honored here, where on Qwen it cannot be.
	case platformFlag == "muse":
		return map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":            museHookEventNameForPhase(phase),
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		}
	default:
		return nil
	}
}

// museHookEventNameForPhase names the Muse Code event the policy seam is answering.
//
// Muse validates that a hook's hookSpecificOutput echoes the event that fired it, so this must
// track the binding in the managed hooks file Beacon writes: PreToolUse for the pre-tool phase,
// PermissionRequest for the permission phase. PreToolUse is the fallback because it is the phase
// the seam runs in on every runtime, so an unrecognized phase degrades to the common case rather
// than to an event name Muse would reject.
func museHookEventNameForPhase(phase policycontract.Phase) string {
	if phase == policycontract.PhasePermissionRequest {
		return "PermissionRequest"
	}
	return "PreToolUse"
}
