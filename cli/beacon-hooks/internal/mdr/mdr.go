// Package mdr is the hook-side client for Asymptote's policy decision endpoint
// (POST /v1/mdr/decide on asymptote-edge). It asks the endpoint what to do about
// an imminent agent action and honors the answer; it holds no policy logic.
//
// Every failure is surfaced as an error so the caller can record that a verdict
// was unavailable, and the caller then allows: a slow or broken service must
// never wedge a developer's session.
package mdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

// Environment overrides. The installed hook reads the config file instead,
// because the settings.json env block is inherited by the agent's Bash tool.
const (
	URLEnv        = "BEACON_MDR_URL"
	TokenEnv      = "BEACON_MDR_TOKEN"
	TimeoutEnv    = "BEACON_MDR_TIMEOUT_MS"
	ConfigPathEnv = "BEACON_POLICY_CONFIG"
)

// Version is the request contract version.
const Version = "1"

// DefaultTimeout bounds a tool-call verdict. The judge runs with default
// reasoning and takes 1.6-4s; the endpoint caps the model at 6s.
const DefaultTimeout = 8 * time.Second

const maxResponseBytes = 64 << 10

var httpClient = &http.Client{}

// Phases.
const (
	PhasePromptSubmit = "prompt-submit"
	PhasePreTool      = "pre-tool"
)

// Decisions.
const (
	DecisionAllow = "allow"
	DecisionAsk   = "ask"
	DecisionDeny  = "deny"
	DecisionSteer = "steer"
	DecisionBlock = "block"
)

// Config is where and how to reach the endpoint.
type Config struct {
	URL     string        `json:"url"`
	Token   string        `json:"token"`
	Timeout time.Duration `json:"-"`
	// TimeoutMS mirrors Timeout in the config file.
	TimeoutMS int `json:"timeout_ms"`
	// Path is the config file the values came from, if any.
	Path string `json:"-"`
}

// Enabled reports whether a decision endpoint is configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

// DefaultConfigPath is ~/.beacon/endpoint/policy.json.
func DefaultConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(ConfigPathEnv)); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", "policy.json")
}

// LoadConfig reads the config file, then applies environment overrides.
func LoadConfig() Config {
	var cfg Config
	if path := DefaultConfigPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &cfg) == nil {
				cfg.Path = path
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv(URLEnv)); v != "" {
		cfg.URL = v
	}
	if v := strings.TrimSpace(os.Getenv(TokenEnv)); v != "" {
		cfg.Token = v
	}
	if v := strings.TrimSpace(os.Getenv(TimeoutEnv)); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			cfg.TimeoutMS = ms
		}
	}
	cfg.Timeout = DefaultTimeout
	if cfg.TimeoutMS > 0 {
		cfg.Timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	return cfg
}

// ToolInput is the part of a tool call the judge sees.
type ToolInput struct {
	Command     string `json:"command,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// ToolCall names the imminent call.
type ToolCall struct {
	Name  string    `json:"name"`
	Input ToolInput `json:"input"`
}

// Prefilter names the local rules that routed the call.
type Prefilter struct {
	RuleIDs  []string `json:"rule_ids"`
	Category string   `json:"category,omitempty"`
}

// Context is recent session context, masked on the endpoint before sending.
type Context struct {
	RecentPrompts   []string `json:"recent_prompts,omitempty"`
	RecentToolCalls []string `json:"recent_tool_calls,omitempty"`
	PriorDecisions  []string `json:"prior_decisions,omitempty"`
}

// Subject identifies the machine and person for the finding.
type Subject struct {
	Hostname string `json:"hostname,omitempty"`
	UserName string `json:"user_name,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
}

// LocalVerdict reports a block the endpoint already made (the prompt gate).
type LocalVerdict struct {
	Detector      string `json:"detector"`
	MaskedExcerpt string `json:"masked_excerpt"`
	Fingerprint   string `json:"fingerprint"`
	Decision      string `json:"decision"`
}

// Request is the POST body.
type Request struct {
	Version        string        `json:"version"`
	Phase          string        `json:"phase"`
	Harness        string        `json:"harness"`
	SessionID      string        `json:"session_id,omitempty"`
	Repository     string        `json:"repository,omitempty"`
	Branch         string        `json:"branch,omitempty"`
	Cwd            string        `json:"cwd,omitempty"`
	Origin         string        `json:"origin,omitempty"`
	Prompt         string        `json:"prompt,omitempty"`
	Tool           *ToolCall     `json:"tool,omitempty"`
	ToolUseID      string        `json:"tool_use_id,omitempty"`
	PermissionMode string        `json:"permission_mode,omitempty"`
	Prefilter      *Prefilter    `json:"prefilter,omitempty"`
	Context        *Context      `json:"context,omitempty"`
	Subject        *Subject      `json:"subject,omitempty"`
	LocalVerdict   *LocalVerdict `json:"local_verdict,omitempty"`
	DryRun         bool          `json:"dry_run,omitempty"`
}

// Response is the verdict. Only Decision is always present.
type Response struct {
	Decision   string `json:"decision"`
	Message    string `json:"message,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Guidance   string `json:"guidance,omitempty"`
	PolicyID   string `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Mode       string `json:"mode,omitempty"`
	LatencyMS  int    `json:"latency_ms,omitempty"`
	FindingID  string `json:"finding_id,omitempty"`
	FindingURL string `json:"finding_url,omitempty"`
}

// Text is what to show the agent (or, on an ask, the developer).
func (r Response) Text() string {
	if m := strings.TrimSpace(r.Message); m != "" {
		return m
	}
	return strings.TrimSpace(r.Reason)
}

// Flagged reports whether a policy matched, whatever the decision.
func (r Response) Flagged() bool { return strings.TrimSpace(r.PolicyID) != "" }

// ErrNotConfigured is returned when no endpoint URL is set.
var ErrNotConfigured = errors.New("policy endpoint not configured")

// Consult POSTs the request and returns the normalized verdict. Any transport,
// status or decoding failure is an error; the caller allows and records it.
func Consult(ctx context.Context, cfg Config, req Request) (Response, error) {
	if !cfg.Enabled() {
		return Response{Decision: DecisionAllow}, ErrNotConfigured
	}
	if req.Version == "" {
		req.Version = Version
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return allow(), err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return allow(), err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "beacon-policy/"+version.Version)
	if token := strings.TrimSpace(cfg.Token); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return allow(), err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return allow(), fmt.Errorf("policy endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return allow(), err
	}
	var decoded Response
	if err := json.Unmarshal(bytes.TrimSpace(body), &decoded); err != nil {
		return allow(), fmt.Errorf("policy endpoint returned malformed JSON: %w", err)
	}
	return normalize(decoded), nil
}

func allow() Response { return Response{Decision: DecisionAllow} }

// normalize collapses anything the caller should not act on into an allow:
// an unrecognized decision, or an actionable one with no text to show. The
// rest of the response (policy, mode, finding) is kept so a shadow-mode
// allow can still be recorded as flagged.
func normalize(resp Response) Response {
	decision := strings.ToLower(strings.TrimSpace(resp.Decision))
	switch decision {
	case DecisionAsk, DecisionDeny, DecisionSteer, DecisionBlock:
		if resp.Text() == "" {
			resp.Decision = DecisionAllow
			return resp
		}
		resp.Decision = decision
	default:
		resp.Decision = DecisionAllow
	}
	return resp
}
