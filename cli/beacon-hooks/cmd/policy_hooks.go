package cmd

// Policy enforcement v0 (Holly POC): three Claude Code hooks built into a
// separate binary, beacon-policy, that sits beside the telemetry hooks.
//
//   policy-prompt  (UserPromptSubmit) blocks a prompt that carries a secret.
//                  Decided on the machine with no network call; the block is
//                  reported afterwards with a masked excerpt only.
//   policy-tool    (PreToolUse) routes secret-touching calls through a local
//                  prefilter to the cloud judge, and applies its verdict.
//   policy-session (SessionStart) records that the policy hook is active.
//
// Every failure allows. A verdict that could not be fetched is recorded as
// policy.unavailable so silent fail-opens are countable.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/mdr"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/policystate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/prefilter"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/secretscan"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/transcript"
	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

const (
	policyContextPrompts   = 3
	policyContextToolCalls = 5
	policyPriorDecisions   = 5
	promptReportTimeout    = 1500 * time.Millisecond
	// promptOverrideWindow is the one clock for a /beacon-allow override: it
	// starts when the prompt is blocked, and both the override and the single
	// resend it allows must happen before it runs out.
	promptOverrideWindow = 10 * time.Minute
	allowCommandName     = "beacon-allow"
)

var policyToolCmd = &cobra.Command{
	Use:    "policy-tool",
	Short:  "PreToolUse policy gate: prefilter locally, ask the policy judge on a match",
	Hidden: true,
	Run:    runPolicyTool,
}

var policyPromptCmd = &cobra.Command{
	Use:    "policy-prompt",
	Short:  "UserPromptSubmit policy gate: block prompts that carry a secret",
	Hidden: true,
	Run:    runPolicyPrompt,
}

var policySessionCmd = &cobra.Command{
	Use:    "policy-session",
	Short:  "SessionStart: record that the policy hook is active",
	Hidden: true,
	Run:    runPolicySession,
}

var policyAllowCmd = &cobra.Command{
	Use:    "policy-allow",
	Short:  "UserPromptExpansion for /beacon-allow: allow the last blocked prompt once, with a reason",
	Hidden: true,
	Run:    runPolicyAllow,
}

var policyResolveCmd = &cobra.Command{
	Use:    "policy-resolve",
	Short:  "Stop/SessionEnd: record the developer's answers to asked tool calls",
	Hidden: true,
	Run:    runPolicyResolve,
}

var policyScanCmd = &cobra.Command{
	Use:    "policy-scan",
	Short:  "Offline: run the prompt scanner or prefilter over JSON lines on stdin",
	Hidden: true,
	Run:    runPolicyScan,
}

func init() {
	rootCmd.AddCommand(policyToolCmd, policyPromptCmd, policySessionCmd, policyAllowCmd, policyResolveCmd, policyScanCmd)
}

// ---------------------------------------------------------------------------
// PreToolUse
// ---------------------------------------------------------------------------

func runPolicyTool(cmd *cobra.Command, args []string) {
	started := time.Now()
	input, err := readStdinJSON()
	if err != nil || platformFlag != "claude" {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	resolvePolicyAsks(input, sessionID, "pre-tool")
	toolName := getFirstStr(input, "tool_name")
	toolInput := resolveToolInput(input)
	rules := prefilter.Load()
	hits := rules.Match(toolName, toolInput)
	if len(hits) == 0 {
		outputJSON(emptyResponse)
		return
	}

	logger := newHookLogger("policy-tool", platformFlag, sessionID)
	cfg := mdr.LoadConfig()
	toolUseID := getFirstStr(input, "tool_use_id")
	target := policyTarget(toolName, toolInput)

	recent := transcript.Read(getFirstStr(input, "transcript_path"), policyContextPrompts, policyContextToolCalls, toolUseID)
	req := mdr.Request{
		Phase:          mdr.PhasePreTool,
		Harness:        platformFlag,
		SessionID:      sessionID,
		Cwd:            resolveCwd(input, platformFlag),
		Repository:     resolveCwd(input, platformFlag),
		Origin:         strings.TrimSpace(os.Getenv("BEACON_ORIGIN")),
		ToolUseID:      toolUseID,
		PermissionMode: getFirstStr(input, "permission_mode"),
		Tool:           &mdr.ToolCall{Name: toolName, Input: maskedToolInput(toolInput)},
		Prefilter:      &mdr.Prefilter{RuleIDs: prefilter.IDs(hits), Category: hits[0].Category},
		Context: &mdr.Context{
			RecentPrompts:   recent.Prompts,
			RecentToolCalls: recent.ToolCalls,
			PriorDecisions:  priorDecisions(sessionID),
		},
		Subject: policySubject(),
	}

	resp, err := mdr.Consult(context.Background(), cfg, req)
	details := map[string]interface{}{
		"rule_ids":      prefilter.IDs(hits),
		"ruleset":       rules.Version + "@" + rules.Hash,
		"hook_ms":       time.Since(started).Milliseconds(),
		"server_ms":     resp.LatencyMS,
		"confidence":    resp.Confidence,
		"mode":          resp.Mode,
		"finding_url":   resp.FindingURL,
		"policy_binary": version.Version,
	}
	if err != nil {
		details["error"] = err.Error()
		emitPolicyEvent(logger, "policy.unavailable", "low", "Policy verdict unavailable; the call was allowed", input, toolName, toolInput, resp, details)
		outputJSON(emptyResponse)
		return
	}

	switch resp.Decision {
	case mdr.DecisionDeny:
		recordPolicyDecision(sessionID, resp, toolName, target)
		emitPolicyEvent(logger, "policy.blocked", "high", firstNonEmpty(resp.Reason, "Tool call blocked by policy"), input, toolName, toolInput, resp, details)
		outputJSON(map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": resp.Text(),
			},
			"systemMessage": policyBanner("blocked", resp),
		})
	case mdr.DecisionAsk:
		_ = policystate.Append(sessionID, policystate.Entry{
			At: time.Now().UTC(), Decision: mdr.DecisionAsk, Tool: toolName, Target: target,
			Reason: clipPolicy(resp.Reason, 300), ToolUseID: toolUseID, PolicyID: resp.PolicyID,
			PolicyName: resp.PolicyName, FindingID: resp.FindingID,
		})
		details["finding_id"] = resp.FindingID
		emitPolicyEvent(logger, "policy.asked", "medium", firstNonEmpty(resp.Reason, "Tool call sent to the developer by policy"), input, toolName, toolInput, resp, details)
		out := map[string]interface{}{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": resp.Text(),
		}
		if ctx := strings.TrimSpace(resp.AgentContext); ctx != "" {
			out["additionalContext"] = ctx
		}
		outputJSON(map[string]interface{}{"hookSpecificOutput": out})
	default:
		action, severity, message := "policy.allowed", "info", "Policy judge allowed the call"
		if resp.Flagged() {
			action, severity, message = "policy.flagged", "medium", firstNonEmpty(resp.Reason, "Policy matched in shadow mode")
		}
		emitPolicyEvent(logger, action, severity, message, input, toolName, toolInput, resp, details)
		outputJSON(emptyResponse)
	}
}

// maskedToolInput is what the judge sees: the fields it needs, with any
// secret literal already masked on the machine.
func maskedToolInput(in map[string]interface{}) mdr.ToolInput {
	get := func(key string) string {
		if v, ok := in[key].(string); ok {
			return secretscan.Mask(v)
		}
		return ""
	}
	out := mdr.ToolInput{
		Command:     get("command"),
		FilePath:    get("file_path"),
		Pattern:     get("pattern"),
		Path:        get("path"),
		URL:         get("url"),
		Description: get("description"),
	}
	if out.Path == "" {
		out.Path = get("glob")
	}
	if out.Command == "" && out.FilePath == "" && out.Pattern == "" && out.Path == "" && out.URL == "" && len(in) > 0 {
		// MCP and other tools: the whole argument object, masked.
		if raw, err := json.Marshal(in); err == nil {
			out.Command = secretscan.Mask(string(raw))
		}
	}
	return out
}

func policyTarget(toolName string, in map[string]interface{}) string {
	t := maskedToolInput(in)
	return clipPolicy(firstNonEmpty(t.Command, t.FilePath, t.Path, t.Pattern, t.URL, toolName), 200)
}

func priorDecisions(sessionID string) []string {
	entries := policystate.Load(sessionID)
	if len(entries) > policyPriorDecisions {
		entries = entries[len(entries)-policyPriorDecisions:]
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		line := fmt.Sprintf("%s %s `%s`: %s", e.Decision, e.Tool, e.Target, e.Reason)
		switch e.Outcome {
		case "approved":
			line += " The developer approved it, overriding the policy"
		case "rejected":
			line += " The developer denied it"
		case "dismissed":
			line += " The developer dismissed the prompt"
		}
		if e.Comment != "" {
			line += fmt.Sprintf(" (comment: %s)", e.Comment)
		}
		out = append(out, line)
	}
	return out
}

func recordPolicyDecision(sessionID string, resp mdr.Response, toolName, target string) {
	_ = policystate.Append(sessionID, policystate.Entry{
		At: time.Now().UTC(), Decision: resp.Decision, Tool: toolName, Target: target, Reason: clipPolicy(resp.Reason, 300),
	})
}

func policyBanner(verb string, resp mdr.Response) string {
	parts := []string{fmt.Sprintf("Beacon %s this tool call", verb)}
	if name := strings.TrimSpace(resp.PolicyName); name != "" {
		parts = append(parts, fmt.Sprintf("policy %q", name))
	}
	if reason := strings.TrimSpace(resp.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if url := strings.TrimSpace(resp.FindingURL); url != "" {
		parts = append(parts, url)
	}
	return "🛡️ " + strings.Join(parts, " · ")
}

// ---------------------------------------------------------------------------
// UserPromptSubmit
// ---------------------------------------------------------------------------

func runPolicyPrompt(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil || platformFlag != "claude" {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	resolvePolicyAsks(input, sessionID, "prompt-submit")
	prompt := getFirstStr(input, "prompt")
	findings := secretscan.Scan(prompt)
	if len(findings) == 0 {
		outputJSON(emptyResponse)
		return
	}
	fingerprints := make([]string, 0, len(findings))
	for _, f := range findings {
		fingerprints = append(fingerprints, f.Fingerprint)
	}
	first := findings[0]
	now := time.Now().UTC()

	// The developer allowed exactly these secrets with /beacon-allow: let the
	// prompt through, once.
	if granted, ok := policystate.ConsumePromptOverride(sessionID, fingerprints, now, promptOverrideWindow); ok {
		outputJSON(emptyResponse)
		logger := newHookLogger("policy-prompt", platformFlag, sessionID)
		fields := sessionFields(sessionID, input)
		fields["policy"] = map[string]interface{}{
			"id": granted.PolicyID, "name": firstNonEmpty(granted.PolicyName, "Secret exposure"),
			"decision": "allow", "enforcement": "override", "reason": granted.Comment,
		}
		setToolCallID(fields, granted.ToolUseID)
		fields["raw"] = map[string]interface{}{"beacon_policy": map[string]interface{}{
			"phase": "prompt-submit", "override_id": granted.ToolUseID, "detections": promptDetections(findings),
			"allowed_at": granted.AllowedAt, "policy_binary": version.Version,
		}}
		emitHookEvent(logger, "policy.override_used", categoryForAction("policy.override_used"), "medium",
			"Prompt with a secret sent under the developer's /beacon-allow override", input, fields)
		return
	}

	outputJSON(map[string]interface{}{
		"decision": "block",
		"reason":   promptBlockReason(findings),
		// Claude Code otherwise repeats the blocked prompt, secret included,
		// under the reason.
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":          "UserPromptSubmit",
			"suppressOriginalPrompt": true,
		},
	})
	// The verdict is on stdout; recording and reporting cannot change it.
	_ = os.Stdout.Sync()

	logger := newHookLogger("policy-prompt", platformFlag, sessionID)
	blockID := "prompt:" + first.Fingerprint
	fields := sessionFields(sessionID, input)
	fields["policy"] = map[string]interface{}{
		"name": "Secret exposure", "decision": "block", "enforcement": "enforce",
		"reason": fmt.Sprintf("Prompt blocked on the endpoint: it contains %s (%s)", first.Label, first.Masked),
	}
	setToolCallID(fields, blockID)
	fields["raw"] = map[string]interface{}{"beacon_policy": map[string]interface{}{
		"phase": "prompt-submit", "detections": promptDetections(findings), "policy_binary": version.Version,
	}}
	emitHookEvent(logger, "policy.blocked", categoryForAction("policy.blocked"), "high",
		fmt.Sprintf("Prompt blocked: it contains %s", first.Label), input, fields)

	cfg := mdr.LoadConfig()
	cfg.Timeout = promptReportTimeout
	resp, _ := mdr.Consult(context.Background(), cfg, mdr.Request{
		Phase:     mdr.PhasePromptSubmit,
		Harness:   platformFlag,
		SessionID: sessionID,
		Cwd:       resolveCwd(input, platformFlag),
		Subject:   policySubject(),
		LocalVerdict: &mdr.LocalVerdict{
			Detector: first.Detector, MaskedExcerpt: first.Masked, Fingerprint: first.Fingerprint, Decision: "block",
		},
	})
	_ = policystate.Append(sessionID, policystate.Entry{
		At: now, Decision: policystate.DecisionBlock, Tool: policystate.ToolPrompt,
		Target: "prompt: " + first.Masked, Reason: fmt.Sprintf("contains %s", first.Label),
		ToolUseID: blockID, PolicyID: resp.PolicyID, PolicyName: resp.PolicyName, Fingerprints: fingerprints,
	})
}

func promptDetections(findings []secretscan.Finding) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(findings))
	for _, f := range findings {
		out = append(out, map[string]interface{}{"detector": f.Detector, "masked": f.Masked, "fingerprint": f.Fingerprint})
	}
	return out
}

// ---------------------------------------------------------------------------
// UserPromptExpansion for /beacon-allow
// ---------------------------------------------------------------------------

// runPolicyAllow handles /beacon-allow <reason>. It grants a one-time
// allowance for the secrets in the session's most recent blocked prompt,
// reports the override and reason, and blocks the command itself so neither
// the command nor the reason reaches the model.
func runPolicyAllow(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil || platformFlag != "claude" || getFirstStr(input, "command_name") != allowCommandName {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	reason := clipPolicy(secretscan.Mask(strings.TrimSpace(getFirstStr(input, "command_args"))), 500)
	block := func(msg string) {
		outputJSON(map[string]interface{}{
			"decision": "block", "reason": msg,
			"hookSpecificOutput": map[string]interface{}{"hookEventName": "UserPromptExpansion", "suppressOriginalPrompt": true},
		})
	}

	now := time.Now().UTC()
	blocked, ok := policystate.LatestPromptBlock(sessionID, now, promptOverrideWindow)
	if !ok {
		block("Nothing to override.\nBeacon hasn't blocked a prompt in this session in the last 10 minutes.")
		return
	}
	if reason == "" {
		block("Add a reason:  /beacon-allow <reason>\nBeacon learns from your reason, so please say why.")
		return
	}
	if err := policystate.GrantPromptOverride(sessionID, blocked.ToolUseID, reason, now); err != nil {
		block("Beacon couldn't record the override on this machine.\nThe prompt stays blocked.")
		return
	}
	left := int(math.Ceil(promptOverrideWindow.Minutes() - now.Sub(blocked.At).Minutes()))
	if left < 1 {
		left = 1
	}
	unit := "minutes"
	if left == 1 {
		unit = "minute"
	}
	block(fmt.Sprintf("Override recorded: %q\nResend the blocked prompt within %d %s. It will go through once.", reason, left, unit))
	_ = os.Stdout.Sync()

	logger := newHookLogger("policy-allow", platformFlag, sessionID)
	fields := sessionFields(sessionID, input)
	fields["policy"] = map[string]interface{}{
		"id": blocked.PolicyID, "name": firstNonEmpty(blocked.PolicyName, "Secret exposure"),
		"decision": "approved", "enforcement": "override", "reason": reason,
	}
	fields["approval"] = map[string]interface{}{"required": true, "decision": "allow", "reason": reason}
	setToolCallID(fields, blocked.ToolUseID)
	fields["raw"] = map[string]interface{}{"beacon_policy": map[string]interface{}{
		"outcome": "approved", "comment": reason, "override_id": blocked.ToolUseID, "blocked": blocked.Target,
		"resolved_via": "beacon-allow", "policy_binary": version.Version,
	}}
	emitHookEvent(logger, "policy.overridden", categoryForAction("policy.overridden"), "medium", reason, input, fields)
	_ = mdr.SendFeedback(context.Background(), mdr.LoadConfig(), mdr.Feedback{
		SessionID: sessionID, ToolUseID: blocked.ToolUseID, Outcome: "approved", Comment: reason,
		PolicyID: blocked.PolicyID, ResolvedVia: "beacon-allow", Subject: policySubject(),
	})
}

func promptBlockReason(findings []secretscan.Finding) string {
	first := findings[0]
	what := fmt.Sprintf("%s (%s)", first.Label, first.Masked)
	if extra := len(findings) - 1; extra == 1 {
		what += " and 1 more secret"
	} else if extra > 1 {
		what += fmt.Sprintf(" and %d more secrets", extra)
	}
	return "A Beacon policy (Secret exposure) blocked this prompt.\n" +
		"It contains " + what + ". Secrets shouldn't be sent to the model.\n" +
		"Refer to the secret by name instead, or inject it with: infisical run -- <command>\n" +
		"\n" +
		"To send it anyway:  /beacon-allow <reason>\n" +
		"Beacon learns from your reason, so please say why."
}

// ---------------------------------------------------------------------------
// SessionStart
// ---------------------------------------------------------------------------

func runPolicySession(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil || platformFlag != "claude" {
		outputJSON(emptyResponse)
		return
	}
	sessionID := resolveSessionID(input, platformFlag)
	logger := newHookLogger("policy-session", platformFlag, sessionID)
	rules := prefilter.Load()
	cfg := mdr.LoadConfig()
	fields := sessionFields(sessionID, input)
	fields["policy"] = map[string]interface{}{"name": "Secret exposure", "decision": "active", "enforcement": "hook"}
	fields["raw"] = map[string]interface{}{"beacon_policy": map[string]interface{}{
		"policy_binary": version.Version, "ruleset": rules.Version + "@" + rules.Hash, "ruleset_source": rules.Source,
		"judge_configured": cfg.Enabled(),
	}}
	emitHookEvent(logger, "policy.active", "session", "info", "Beacon policy hook active", input, fields)
	outputJSON(emptyResponse)
}

// ---------------------------------------------------------------------------
// The developer's answer to an asked call
// ---------------------------------------------------------------------------

func runPolicyResolve(cmd *cobra.Command, args []string) {
	input, err := readStdinJSON()
	if err != nil || platformFlag != "claude" {
		outputJSON(emptyResponse)
		return
	}
	via := strings.ToLower(firstNonEmpty(getFirstStr(input, "hook_event_name"), "resolve"))
	resolvePolicyAsks(input, resolveSessionID(input, platformFlag), via)
	outputJSON(emptyResponse)
}

const (
	rejectionMarker = "The user doesn't want to proceed with this tool use"
	commentMarker   = "the user said:"
)

// classifyAnswer reads the developer's answer from the result the agent got.
//
// Claude Code records a No as an error result that starts with rejectionMarker,
// with any comment after "the user said:". Anything else means the call ran,
// so the developer approved it; a comment attached to an approval follows the
// result in the same message.
func classifyAnswer(res transcript.ToolResult) (outcome, comment string) {
	content := strings.TrimSpace(res.Content)
	switch {
	case strings.Contains(content, rejectionMarker):
		if i := strings.Index(content, commentMarker); i >= 0 {
			comment = content[i+len(commentMarker):]
			// Claude Code appends its own guidance after the developer's words,
			// separated by a blank line and starting "Note:".
			if j := strings.Index(comment, "\n\nNote: "); j >= 0 {
				comment = comment[:j]
			}
			comment = strings.TrimSpace(comment)
		}
		return "rejected", comment
	case strings.HasPrefix(content, "[Request interrupted"):
		return "dismissed", ""
	default:
		return "approved", strings.TrimSpace(res.After)
	}
}

// resolvePolicyAsks finds the session's asked calls whose result is now in the
// transcript, and records the developer's answer: locally as a policy event
// carrying the original call's tool_use_id, and on the finding through the
// feedback endpoint. Unanswered asks stay pending for the next hook.
func resolvePolicyAsks(input map[string]interface{}, sessionID, via string) {
	pending := policystate.Pending(sessionID)
	if len(pending) == 0 {
		return
	}
	ids := map[string]bool{}
	for _, p := range pending {
		ids[p.ToolUseID] = true
	}
	results := transcript.ToolResults(getFirstStr(input, "transcript_path"), ids)
	if len(results) == 0 {
		return
	}
	logger := newHookLogger("policy-resolve", platformFlag, sessionID)
	cfg := mdr.LoadConfig()
	subject := policySubject()
	for _, p := range pending {
		res, ok := results[p.ToolUseID]
		if !ok {
			continue
		}
		outcome, comment := classifyAnswer(res)
		comment = clipPolicy(secretscan.Mask(comment), 1000)
		now := time.Now().UTC()
		_ = policystate.Answer(sessionID, p.ToolUseID, outcome, comment, now)
		emitPolicyAnswer(logger, input, sessionID, p, outcome, comment, via)
		_ = mdr.SendFeedback(context.Background(), cfg, mdr.Feedback{
			SessionID: sessionID, ToolUseID: p.ToolUseID, Outcome: outcome, Comment: comment,
			FindingID: p.FindingID, PolicyID: p.PolicyID, ResolvedVia: via, Subject: subject,
		})
	}
}

var answerActions = map[string]string{"approved": "policy.overridden", "rejected": "policy.upheld", "dismissed": "policy.dismissed"}

func emitPolicyAnswer(logger *logging.Logger, input map[string]interface{}, sessionID string, p policystate.Entry, outcome, comment, via string) {
	// The writer takes the event's call ID from the hook payload, which belongs
	// to whichever hook is running now. Hand it the asked call's ID instead.
	envelope := cloneFields(input)
	envelope["tool_use_id"] = p.ToolUseID
	fields := sessionFields(sessionID, input)
	target := map[string]interface{}{"command": p.Target}
	if p.Tool == "Bash" {
		fields["command"] = target
		fields["tool"] = map[string]interface{}{"name": p.Tool, "command": p.Target}
	} else {
		fields["tool"] = map[string]interface{}{"name": p.Tool}
		fields["file"] = map[string]interface{}{"path": p.Target}
	}
	reason := firstNonEmpty(comment, map[string]string{
		"approved":  "The developer approved the call, overriding the policy",
		"rejected":  "The developer denied the call, agreeing with the policy",
		"dismissed": "The developer dismissed the prompt",
	}[outcome])
	fields["policy"] = map[string]interface{}{"id": p.PolicyID, "name": p.PolicyName, "decision": outcome, "enforcement": "ask", "reason": reason}
	approval := "deny"
	if outcome == "approved" {
		approval = "allow"
	}
	fields["approval"] = map[string]interface{}{"required": true, "decision": approval, "reason": reason}
	fields["raw"] = map[string]interface{}{"beacon_policy": map[string]interface{}{
		"outcome": outcome, "comment": comment, "tool_use_id": p.ToolUseID, "finding_id": p.FindingID,
		"asked_at": p.At, "resolved_via": via, "policy_binary": version.Version,
	}}
	severity := "info"
	if outcome == "approved" {
		severity = "medium"
	}
	emitHookEvent(logger, answerActions[outcome], categoryForAction(answerActions[outcome]), severity, reason, envelope, fields)
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

func emitPolicyEvent(logger *logging.Logger, action, severity, message string, input map[string]interface{}, toolName string, toolInput map[string]interface{}, resp mdr.Response, details map[string]interface{}) {
	sessionID := resolveSessionID(input, platformFlag)
	fields := sessionFields(sessionID, input)
	mergeMap(fields, toolFields(toolName, toolInput))
	enforcement := "enforce"
	if resp.Mode == "monitor" {
		enforcement = "monitor"
	}
	policy := map[string]interface{}{"decision": resp.Decision, "enforcement": enforcement}
	if resp.PolicyID != "" {
		policy["id"] = resp.PolicyID
	}
	if resp.PolicyName != "" {
		policy["name"] = resp.PolicyName
	}
	if reason := firstNonEmpty(resp.Reason, message); reason != "" {
		policy["reason"] = reason
	}
	fields["policy"] = policy
	if action == "policy.blocked" {
		fields["approval"] = map[string]interface{}{"required": true, "decision": "deny", "reason": policy["reason"]}
	}
	fields["raw"] = map[string]interface{}{"beacon_policy": details}
	emitHookEvent(logger, action, categoryForAction(action), severity, message, input, fields)
}

func policySubject() *mdr.Subject {
	s := &mdr.Subject{}
	if host, err := os.Hostname(); err == nil {
		s.Hostname = host
	}
	if u, err := user.Current(); err == nil {
		s.UserName = u.Username
	}
	if path := strings.TrimSpace(os.Getenv("BEACON_ENDPOINT_CONFIG")); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var cfg struct {
				ManagedIngest struct {
					DeviceID string `json:"device_id"`
				} `json:"managed_ingest"`
			}
			if json.Unmarshal(data, &cfg) == nil {
				s.DeviceID = cfg.ManagedIngest.DeviceID
			}
		}
	}
	return s
}

func clipPolicy(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// ---------------------------------------------------------------------------
// Offline scan (corpus checks)
// ---------------------------------------------------------------------------

// runPolicyScan reads JSON lines from stdin and prints one JSON line per input
// that the prompt scanner (lines with "text") or the prefilter (lines with
// "tool" and "input") flags. Masked excerpts only.
func runPolicyScan(cmd *cobra.Command, args []string) {
	rules := prefilter.Load()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var row struct {
			ID    string                 `json:"id"`
			Text  string                 `json:"text"`
			Tool  string                 `json:"tool"`
			Input map[string]interface{} `json:"input"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		if row.Tool != "" {
			if hits := rules.Match(row.Tool, row.Input); len(hits) > 0 {
				_ = enc.Encode(map[string]interface{}{"id": row.ID, "rule_ids": prefilter.IDs(hits), "target": policyTarget(row.Tool, row.Input)})
			}
			continue
		}
		if findings := secretscan.Scan(row.Text); len(findings) > 0 {
			out := make([]map[string]string, 0, len(findings))
			for _, f := range findings {
				snippet := secretscan.Mask(row.Text[max(0, f.Start-40):min(len(row.Text), f.End+40)])
				out = append(out, map[string]string{"detector": f.Detector, "masked": f.Masked, "context": snippet})
			}
			_ = enc.Encode(map[string]interface{}{"id": row.ID, "findings": out})
		}
	}
}
