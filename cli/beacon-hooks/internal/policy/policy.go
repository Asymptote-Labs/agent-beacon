// Package policy is the hook-side client for the Beacon policy seam. It consults
// an external provider named by BEACON_POLICY_PROVIDER for an allow/deny decision
// on an imminent tool call. It contains no enforcement decision logic of its own —
// it only asks and honors.
//
// The client is inert when no provider is configured, and fail-open on every
// error path (missing provider, marshal failure, exec failure, timeout, non-zero
// exit, malformed output), so the open Beacon build never blocks a tool call by
// default.
package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/policycontract"
)

// ProviderEnv names the executable consulted for policy decisions. When unset or
// empty, the seam is disabled.
const ProviderEnv = "BEACON_POLICY_PROVIDER"

// Timeout bounds how long a hook waits for a provider decision before failing
// open. It is a package variable so tests can shorten it.
var Timeout = 2 * time.Second

// execCommandContext is overridable in tests.
var execCommandContext = exec.CommandContext

// Request carries everything the client needs to consult the provider.
type Request struct {
	Phase    policycontract.Phase
	Platform string
	Event    asymptoteobserve.Event
}

// Enabled reports whether a provider is configured. Hooks use this to skip all
// candidate-building work on the default (no-provider) path.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(ProviderEnv)) != ""
}

// Evaluate consults the configured provider and returns its response. It is
// fail-open: on any error or when no provider is configured it returns an allow
// response.
func Evaluate(ctx context.Context, req Request) policycontract.Response {
	allow := policycontract.Response{Decision: policycontract.DecisionAllow}

	provider := strings.TrimSpace(os.Getenv(ProviderEnv))
	if provider == "" {
		return allow
	}

	payload, err := json.Marshal(policycontract.Request{
		Version:  policycontract.Version,
		Phase:    req.Phase,
		Platform: req.Platform,
		Event:    req.Event,
	})
	if err != nil {
		return allow
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := execCommandContext(ctx, provider)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return allow
	}

	var resp policycontract.Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		return allow
	}
	if !resp.Denied() {
		return allow
	}
	return resp
}
