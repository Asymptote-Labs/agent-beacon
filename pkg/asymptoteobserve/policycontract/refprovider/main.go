// Command refprovider is a reference Beacon policy provider. It demonstrates the
// BEACON_POLICY_PROVIDER contract: read a policycontract.Request from stdin, write
// a policycontract.Response to stdout.
//
// This reference denies a single pattern — spawning an AI agent runtime with its
// permission/approval gate disabled (the open detect-twin's enforce counterpart) —
// and allows everything else. It is intentionally tiny and dependency-light so it
// can be copied as a starting point for a real provider. It is not installed by
// the Beacon build and has no effect unless an operator points
// BEACON_POLICY_PROVIDER at it.
package main

import (
	"encoding/json"
	"os"
	"regexp"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// agentRuntime matches a known agent CLI invoked as a command head.
var agentRuntime = regexp.MustCompile(`(?i)(^|[;&|]|&&|\|\||\bsudo\s+|\benv\s+|\bnpx\s+)\s*(claude|codex|cursor-agent|aider|goose|opencode|gemini)\b`)

// bypassFlag matches a flag that disables the runtime's permission/approval gate.
var bypassFlag = regexp.MustCompile(`(?i)(--dangerously-skip-permissions|--dangerously-bypass-approvals-and-sandbox|--permission-mode[ =]+bypasspermissions|--sandbox[ =]+danger-full-access|--ask-for-approval[ =]+never|--full-auto|--yolo|(^|\s)-a[ =]+never\b|--force\b)`)

func main() {
	resp := decide(os.Stdin)
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}

func decide(r *os.File) policycontract.Response {
	allow := policycontract.Response{Decision: policycontract.DecisionAllow}

	var req policycontract.Request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return allow // fail-open: a provider that cannot parse should not block
	}
	if req.Event.Command == nil {
		return allow
	}
	command := req.Event.Command.Command
	if agentRuntime.MatchString(command) && bypassFlag.MatchString(command) {
		return policycontract.Response{
			Decision: policycontract.DecisionDeny,
			Reason:   "AI agent runtime spawned with its permission/approval gate disabled",
			RuleID:   "agent-permission-bypass-spawn",
			Severity: "high",
			Mode:     "enforce",
		}
	}
	return allow
}
