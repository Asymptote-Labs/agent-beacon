// Package policycontract defines the stable, versioned wire contract between a
// Beacon hook (the asker) and an external policy provider (the decider).
//
// The provider is an executable named by the BEACON_POLICY_PROVIDER environment
// variable. Before a hook allows a tool call, it writes a single JSON Request to
// the provider's stdin and reads a single JSON Response from stdout. The provider
// decides allow vs deny; the hook only asks and honors.
//
// The seam is inert when no provider is configured, and fail-open on any error
// (missing provider, timeout, non-zero exit, malformed output) — so the open
// Beacon build ships no enforcement behavior of its own. Both audit and enforce
// behavior live entirely in whatever provider an operator chooses to install.
//
// This package contains only data types: it is safe to import from any module
// (the hooks, the public CLI, or a third-party provider) without pulling in
// runtime behavior.
package policycontract

import "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"

// Version is the contract version carried in every Request. Providers should
// treat an unrecognized version conservatively (typically by allowing).
const Version = "1"

// Phase identifies which hook is consulting the provider.
type Phase string

const (
	// PhasePreTool is a pre-tool-use consultation: a tool call is about to run.
	PhasePreTool Phase = "pre-tool"
	// PhasePermissionRequest is an explicit permission/approval decision request.
	PhasePermissionRequest Phase = "permission-request"
)

// Decision is the provider's verdict.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Request is written to the provider's stdin as a single JSON object. Event is a
// Beacon endpoint Event describing the imminent tool call (its action, command,
// file, tool, session, and so on) using the same field names the runtime JSONL
// log uses, so a provider can match it with the open Threat Rules format.
type Request struct {
	Version  string                 `json:"version"`
	Phase    Phase                  `json:"phase"`
	Platform string                 `json:"platform"`
	Event    asymptoteobserve.Event `json:"event"`
}

// Response is read from the provider's stdout as a single JSON object. Only
// Decision is required. An empty or unrecognized Decision is treated as allow.
//
//   - Reason:   human-readable explanation surfaced to the runtime and telemetry.
//   - RuleID:   identifier of the provider rule that produced the decision.
//   - Severity: provider-reported severity (e.g. low/medium/high/critical).
//   - Mode:     provider-reported posture (e.g. "audit" or "enforce").
type Response struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	RuleID   string   `json:"rule_id,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Mode     string   `json:"mode,omitempty"`
}

// Denied reports whether the response explicitly denies the action. Any value
// other than an explicit "deny" is treated as allow (fail-open).
func (r Response) Denied() bool { return r.Decision == DecisionDeny }
