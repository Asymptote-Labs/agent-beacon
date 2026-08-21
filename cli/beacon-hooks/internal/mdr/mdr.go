// Package mdr is the hook-side client for Asymptote MDR Tier 2 detection: a
// synchronous, cloud-side verdict on an imminent agent action. It asks a remote
// decision endpoint what to do and honors the answer; it contains no detection
// logic of its own.
//
// It is a deliberate sibling of, not an extension of, the local policy seam in
// internal/policy. That seam delegates to a local executable over stdin/stdout
// and speaks the stable policycontract wire format, whose rule is "anything that
// is not an explicit deny is an allow". Tier 2 needs a third outcome (steer) and
// a cloud transport, so it gets its own contract rather than widening a shipped
// one.
//
// The client is inert when BEACON_MDR_URL is unset, and fail-open on every error
// path (no URL, marshal failure, request construction, dial error, timeout,
// non-2xx status, unreadable body, malformed JSON, unrecognized decision, or an
// actionable decision carrying no text). A hook therefore never blocks a turn
// because this service is slow, broken, or absent.
package mdr

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

// Environment variables configuring the client. When URLEnv is unset or empty,
// the seam is disabled and Decide short-circuits to allow.
const (
	URLEnv     = "BEACON_MDR_URL"
	TokenEnv   = "BEACON_MDR_TOKEN"
	TimeoutEnv = "BEACON_MDR_TIMEOUT_MS"
)

// Version is the contract version carried in every Request so the service can
// reject or downgrade shapes it does not recognize.
const Version = "1"

// DefaultTimeout bounds how long a hook waits for a verdict before failing open.
// The MDR design budgets roughly three seconds for Tier 2.
const DefaultTimeout = 3 * time.Second

// maxResponseBytes caps how much of a response body is read, so a misbehaving
// or hostile endpoint cannot exhaust hook memory.
const maxResponseBytes = 64 << 10

// httpClient is a package variable so tests can substitute a transport.
var httpClient = &http.Client{}

// Phase identifies which hook is consulting the service.
type Phase string

// PhasePromptSubmit is a prompt-submission consultation: the user has submitted
// a prompt and the agent has not yet acted on it.
const PhasePromptSubmit Phase = "prompt-submit"

// Decision is the service's verdict.
type Decision string

const (
	// DecisionAllow proceeds with the turn unchanged.
	DecisionAllow Decision = "allow"
	// DecisionSteer proceeds, injecting Message as context the agent must follow.
	DecisionSteer Decision = "steer"
	// DecisionBlock rejects the prompt outright, surfacing Message as the reason.
	DecisionBlock Decision = "block"
)

// Request is POSTed as a single JSON object. It is intentionally flat: at
// prompt-submit time there is no tool, file, or command to describe, so wrapping
// the prompt in a full endpoint Event envelope would carry mostly empty fields.
type Request struct {
	Version    string `json:"version"`
	Phase      Phase  `json:"phase"`
	Harness    string `json:"harness"`
	SessionID  string `json:"session_id,omitempty"`
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Prompt     string `json:"prompt"`
}

// Response is read from the response body as a single JSON object. Only Decision
// is required; an empty or unrecognized value is treated as allow.
//
//   - Message:    the text surfaced to the agent, and the reason on a block.
//   - Reason:     one-line explanation for operators, banners, and telemetry.
//   - PolicyID:   identifier of the policy that produced the decision.
//   - PolicyName: human-readable policy name, shown to the developer.
//   - Severity:   service-reported severity (low/medium/high/critical).
//   - Mode:       service-reported posture, "enforce" or "monitor".
//   - LatencyMS:  service-measured evaluation time, for the banner.
type Response struct {
	Decision   Decision `json:"decision"`
	Message    string   `json:"message,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	PolicyID   string   `json:"policy_id,omitempty"`
	PolicyName string   `json:"policy_name,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	LatencyMS  int      `json:"latency_ms,omitempty"`
}

// Steered reports whether the response asks the caller to inject guidance.
func (r Response) Steered() bool { return r.Decision == DecisionSteer }

// Blocked reports whether the response asks the caller to reject the prompt.
func (r Response) Blocked() bool { return r.Decision == DecisionBlock }

// Actionable reports whether the response requires the caller to do anything at
// all. An allow is not actionable.
func (r Response) Actionable() bool { return r.Steered() || r.Blocked() }

// Guidance is the text to surface to the agent, preferring the service's
// composed message and falling back to its one-line reason.
func (r Response) Guidance() string {
	if message := strings.TrimSpace(r.Message); message != "" {
		return message
	}
	return strings.TrimSpace(r.Reason)
}

// Enabled reports whether a decision endpoint is configured. Hooks use this to
// skip all request-building work on the default (unconfigured) path.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(URLEnv)) != ""
}

// Timeout returns the configured client budget, falling back to DefaultTimeout
// when BEACON_MDR_TIMEOUT_MS is unset, unparseable, or non-positive.
func Timeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(TimeoutEnv))
	if raw == "" {
		return DefaultTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return DefaultTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// allow is the fail-open response returned from every error path.
func allow() Response { return Response{Decision: DecisionAllow} }

// Decide consults the configured endpoint and returns its verdict. It is
// fail-open: on any error, or when no endpoint is configured, it returns an
// allow response.
func Decide(ctx context.Context, req Request) Response {
	url := strings.TrimSpace(os.Getenv(URLEnv))
	if url == "" {
		return allow()
	}
	if req.Version == "" {
		req.Version = Version
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return allow()
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout())
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return allow()
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "beacon-hooks/"+version.Version)
	if token := strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return allow()
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return allow()
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return allow()
	}

	var decoded Response
	if err := json.Unmarshal(bytes.TrimSpace(body), &decoded); err != nil {
		return allow()
	}
	return normalize(decoded)
}

// normalize collapses anything the caller should not act on into an allow: an
// unrecognized decision, or an actionable decision with no text to surface.
// Casing is tolerated because a verdict is too important to drop over "Steer".
func normalize(resp Response) Response {
	switch Decision(strings.ToLower(strings.TrimSpace(string(resp.Decision)))) {
	case DecisionSteer:
		resp.Decision = DecisionSteer
	case DecisionBlock:
		resp.Decision = DecisionBlock
	default:
		return allow()
	}
	if resp.Guidance() == "" {
		return allow()
	}
	return resp
}
