package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/mdr"
)

// evaluateMDR consults the Asymptote MDR Tier 2 service about a submitted prompt.
// When the verdict is actionable and the current platform has a response shape for
// it, it records the decision as endpoint telemetry and returns that response plus
// true. Otherwise it returns nil, false and the caller proceeds with the normal
// allow flow. It is a no-op (nil, false) when no endpoint is configured.
func evaluateMDR(logger *logging.Logger, input map[string]interface{}, sessionID, prompt string) (map[string]interface{}, bool) {
	if !mdr.Enabled() {
		return nil, false
	}
	resp := mdr.Decide(context.Background(), mdr.Request{
		Phase:      mdr.PhasePromptSubmit,
		Harness:    platformFlag,
		SessionID:  sessionID,
		Repository: resolveCwd(input, platformFlag),
		Branch:     resolveBranch(input, resolveCwd(input, platformFlag)),
		Cwd:        resolveCwd(input, platformFlag),
		Origin:     mdrOrigin(),
		Prompt:     prompt,
	})
	if !resp.Actionable() {
		return nil, false
	}
	hookResponse := mdrHookResponse(resp)
	if hookResponse == nil {
		// Platform has no prompt-submit response shape: honor "unknown platform -> allow".
		logger.Debug("MDR verdict dropped: no response shape for platform",
			"platform", platformFlag, "decision", string(resp.Decision))
		return nil, false
	}
	emitMDRDecision(logger, input, sessionID, prompt, resp)
	return hookResponse, true
}

// mdrOrigin reports the run origin the same way the telemetry envelope does, so
// the service can tell a cloud sandbox from a developer laptop.
func mdrOrigin() string {
	return strings.TrimSpace(os.Getenv("BEACON_ORIGIN"))
}

// mdrHookResponse renders the runtime-specific hook response for an actionable
// verdict. A nil return means the platform has no confirmed prompt-submit shape,
// so the caller allows.
//
// Claude Code is the only runtime wired today. A steer rides in
// hookSpecificOutput.additionalContext, which UserPromptSubmit injects into the
// turn's context for the agent to act on; a block uses the top-level decision
// field, which rejects the prompt before the agent sees it. Both carry a
// systemMessage banner so the developer sees that a policy fired and why.
func mdrHookResponse(resp mdr.Response) map[string]interface{} {
	guidance := resp.Guidance()
	if guidance == "" {
		return nil
	}
	if platformFlag != "claude" {
		return nil
	}
	switch {
	case resp.Steered():
		return map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":     "UserPromptSubmit",
				"additionalContext": guidance,
			},
			"systemMessage": mdrBanner(resp),
		}
	case resp.Blocked():
		return map[string]interface{}{
			"decision":      "block",
			"reason":        guidance,
			"systemMessage": mdrBanner(resp),
		}
	default:
		return nil
	}
}

// mdrBanner is the one-line notice shown to the developer. Optional details are
// omitted rather than rendered empty, so a sparse verdict still reads cleanly.
func mdrBanner(resp mdr.Response) string {
	parts := []string{"tier 2"}
	if resp.LatencyMS > 0 {
		parts = append(parts, fmt.Sprintf("%dms", resp.LatencyMS))
	}
	if name := strings.TrimSpace(resp.PolicyName); name != "" {
		parts = append(parts, fmt.Sprintf("policy %q", name))
	} else if reason := strings.TrimSpace(resp.Reason); reason != "" {
		parts = append(parts, reason)
	}
	verb := "steered"
	if resp.Blocked() {
		verb = "blocked"
	}
	return fmt.Sprintf("🛡️ Asymptote MDR: %s · %s", verb, strings.Join(parts, " · "))
}

// emitMDRDecision records the verdict as endpoint telemetry, carrying the policy
// identity and posture the service reported. It reuses the existing PolicyInfo
// fields, so the event schema is unchanged.
func emitMDRDecision(logger *logging.Logger, input map[string]interface{}, sessionID, prompt string, resp mdr.Response) {
	fields := sessionFields(sessionID, input)
	if prompt != "" {
		fields["prompt"] = map[string]interface{}{"text": prompt}
	}

	reason := strings.TrimSpace(resp.Reason)
	if reason == "" {
		reason = resp.Guidance()
	}
	policyField := map[string]interface{}{
		"decision": string(resp.Decision),
		"reason":   reason,
	}
	if enforcement := strings.TrimSpace(resp.Mode); enforcement != "" {
		policyField["enforcement"] = enforcement
	}
	if id := strings.TrimSpace(resp.PolicyID); id != "" {
		policyField["id"] = id
	}
	if name := strings.TrimSpace(resp.PolicyName); name != "" {
		policyField["name"] = name
	}
	fields["policy"] = policyField

	action := "policy.steered"
	if resp.Blocked() {
		action = "policy.blocked"
		// A blocked prompt is also an approval outcome, so mirror the shape the
		// pre-tool deny path uses and keep approval-based queries complete.
		fields["approval"] = map[string]interface{}{
			"required": true,
			"decision": "deny",
			"reason":   reason,
		}
	}

	severity := strings.TrimSpace(resp.Severity)
	if severity == "" {
		severity = "high"
	}

	message := reason
	if message == "" {
		message = "Prompt matched an Asymptote MDR policy"
	}
	emitHookEvent(logger, action, categoryForAction(action), severity, message, input, fields)
}
