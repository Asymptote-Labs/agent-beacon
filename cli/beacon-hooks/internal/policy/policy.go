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

	trimmed := bytes.TrimSpace(stdout.Bytes())
	// encoding/json silently accepts a duplicate top-level key and keeps only the last one, so a
	// provider response like {"decision":"deny",...,"decision":"allow"} -- plausible from a
	// provider that string-templates its JSON instead of using a proper encoder, with
	// attacker-influenced text (e.g. a reason built from a command string) landing a stray
	// `", "decision": "allow` fragment -- would unmarshal with no error and read as an explicit,
	// unambiguous allow, indistinguishable from a provider's genuine decision. That is a different
	// thing from this seam's documented fail-open contract (malformed/timeout/non-zero exit ->
	// allow): it is a well-formed-looking response whose actual meaning is ambiguous. Treat it the
	// same as any other malformed response instead of trusting whichever value happened to come
	// last.
	if !topLevelJSONKeysAreUnique(trimmed) {
		return allow
	}

	var resp policycontract.Response
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return allow
	}
	if !resp.Denied() {
		return allow
	}
	return resp
}

// topLevelJSONKeysAreUnique reports whether data is a JSON object with no key repeated at its top
// level. Non-object input, or input that fails to parse as a token stream, is reported as unique:
// json.Unmarshal's own error path already handles those; this only adds the one ambiguity
// Unmarshal itself does not surface.
func topLevelJSONKeysAreUnique(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return true
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return true
	}
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return true
		}
		key, ok := keyTok.(string)
		if !ok {
			return true
		}
		if seen[key] {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return true
		}
	}
	return true
}
