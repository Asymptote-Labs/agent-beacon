// Package intelapi is the client for Beacon Intel (https://intel.beacon.sh), the free public
// index of the MCP servers and agent skills that public registries list, with a security
// verdict for each one that has been analyzed. It is shared by everything that gives agents
// that index: `beacon intel` and the local `beacon` MCP server in agent-beacon, and Beacon Cloud
// MCP, so the API contract, its limits, the request hardening and the tools' wording live in one
// place (tools.go).
//
// The API needs no account and no key, and this client sends none: a request carries only what
// the caller asks about (a name, a repository, a package, an entity key) and the caller's
// User-Agent. Redirects are refused, so a request never leaves the configured host; answers are
// capped and must be marked as the intel.v1 API. Where a caller gets its host from and whether
// it may call at all (BEACON_INTEL, BEACON_INTEL_URL, INTEL_API_URL) is the caller's business.
//
// Text in a response comes from registries, repositories and model analysis. The API clips it
// to bounded single lines; callers still treat it as data, never as instructions.
package intelapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is the public Beacon Intel host.
	DefaultBaseURL = "https://intel.beacon.sh"
	// APIVersion is the "api" value every answer of the v1 API carries.
	APIVersion = "intel.v1"

	// MaxCheck is the most artifacts one check may name; the API refuses more.
	MaxCheck = 50
	// MaxQuery is the longest lookup string the API accepts.
	MaxQuery = 300
	// MaxLimit is the most results one lookup returns; DefaultLimit what a lookup asks for when
	// the caller names no limit.
	MaxLimit     = 25
	DefaultLimit = 10

	maxResponseBytes = 4 << 20
	requestTimeout   = 15 * time.Second
)

// Client calls the Beacon Intel API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
}

// New returns a client for baseURL (see NormalizeBaseURL). A nil httpClient gets one with a
// 15-second timeout; either way, redirects are never followed.
func New(baseURL, userAgent string, httpClient *http.Client) (*Client, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{BaseURL: normalized, HTTPClient: httpClient, UserAgent: userAgent}, nil
}

// NormalizeBaseURL accepts an https origin, or an http one on a loopback host, with no path,
// credentials, query or fragment, and returns it without a trailing slash.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not an absolute URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("%q must be a bare origin such as %s", raw, DefaultBaseURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("%q must use https (plain http is allowed only for loopback hosts)", raw)
		}
	default:
		return "", fmt.Errorf("%q must use https", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Summary is one artifact as lookup and check list it.
type Summary struct {
	EntityKey       string   `json:"entity_key"`
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Repository      string   `json:"repository"`
	CodePath        string   `json:"code_path"`
	Registries      []string `json:"registries"`
	Risk            string   `json:"risk"`
	Guidance        string   `json:"guidance"`
	AnalyzedAt      string   `json:"analyzed_at"`
	RepositoryFlags []string `json:"repository_flags"`
	Stars           *int     `json:"stars"`
	URL             string   `json:"url"`
}

// LookupResult is GET /api/v1/lookup.
type LookupResult struct {
	Query struct {
		Q         string `json:"q"`
		Kind      string `json:"kind"`
		Limit     int    `json:"limit"`
		MatchedBy string `json:"matched_by"`
	} `json:"query"`
	Results []Summary `json:"results"`
	More    bool      `json:"more"`
	Note    string    `json:"note"`
}

// Finding is one finding of a verdict.
type Finding struct {
	ID          string `json:"id"`
	Class       string `json:"class"`
	ClassLabel  string `json:"class_label"`
	Severity    string `json:"severity"`
	Confidence  string `json:"confidence"`
	Disposition string `json:"disposition"`
	Path        string `json:"path"`
	Lines       []int  `json:"lines"`
	Claim       string `json:"claim"`
}

// Artifact is GET /api/v1/artifact's artifact. Fields the CLI does not print stay in the raw
// answer the caller also gets.
type Artifact struct {
	EntityKey   string   `json:"entity_key"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Repository  string   `json:"repository"`
	CodePath    string   `json:"code_path"`
	URL         string   `json:"url"`
	Registries  []string `json:"registries"`
	Risk        string   `json:"risk"`
	Guidance    string   `json:"guidance"`
	Verdict     *struct {
		Verdict          string    `json:"verdict"`
		AnalyzedAt       string    `json:"analyzed_at"`
		Model            string    `json:"model"`
		Commit           string    `json:"commit"`
		CoverageComplete bool      `json:"coverage_complete"`
		Reasons          []string  `json:"reasons"`
		Unresolved       []string  `json:"unresolved"`
		Findings         []Finding `json:"findings"`
		FindingsTotal    int       `json:"findings_total"`
		RefutedFindings  int       `json:"refuted_findings"`
	} `json:"verdict"`
	RepositoryLineage *struct {
		FullName      string   `json:"full_name"`
		Flags         []string `json:"flags"`
		PreviousNames []string `json:"previous_names"`
	} `json:"repository_lineage"`
	Advisories []struct {
		Ecosystem  string `json:"ecosystem"`
		Package    string `json:"package"`
		Version    string `json:"version"`
		AdvisoryID string `json:"advisory_id"`
		Severity   string `json:"severity"`
		Malicious  bool   `json:"malicious"`
		Summary    string `json:"summary"`
	} `json:"advisories"`
	AdvisoriesTotal   int `json:"advisories_total"`
	RiskyDependencies []struct {
		Repository string   `json:"repository"`
		Flags      []string `json:"flags"`
	} `json:"risky_dependencies"`
	ExecutionChain map[string][]string `json:"execution_chain"`
	Note           string              `json:"note"`
}

type artifactEnvelope struct {
	Artifact Artifact `json:"artifact"`
}

// CheckItem is one artifact to check: a lookup string and, optionally, its kind.
type CheckItem struct {
	Q    string `json:"q"`
	Kind string `json:"kind,omitempty"`
}

// CheckOutcome is one item of POST /api/v1/check.
type CheckOutcome struct {
	Q         string    `json:"q"`
	MatchedBy string    `json:"matched_by"`
	Status    string    `json:"status"`
	Risk      string    `json:"risk"`
	Guidance  string    `json:"guidance"`
	Matches   []Summary `json:"matches"`
	More      bool      `json:"more"`
}

// CheckResult is POST /api/v1/check.
type CheckResult struct {
	Results []CheckOutcome `json:"results"`
	Counts  map[string]int `json:"counts"`
	Note    string         `json:"note"`
}

// Status is GET /api/v1/status.
type Status struct {
	Index map[string]struct {
		Artifacts    int `json:"artifacts"`
		Analyzed     int `json:"analyzed"`
		Malicious    int `json:"malicious"`
		Suspicious   int `json:"suspicious"`
		Inconclusive int `json:"inconclusive"`
		Benign       int `json:"benign"`
	} `json:"index"`
	LatestAnalyzedAt string `json:"latest_analyzed_at"`
	LatestListedAt   string `json:"latest_listed_at"`
}

// APIError is an answer other than 200. NotFound reports a 404.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Beacon Intel: %s (HTTP %d)", e.Message, e.StatusCode)
	}
	return fmt.Sprintf("Beacon Intel answered HTTP %d", e.StatusCode)
}

// NotFound reports whether err is the API's 404.
func NotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// ValidateKind accepts "", "mcp" or "skill".
func ValidateKind(kind string) error {
	switch kind {
	case "", "mcp", "skill":
		return nil
	}
	return inputErrorf("kind must be mcp or skill, not %q", clipLine(kind, 40))
}

func validateQuery(name, q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", inputErrorf("%s is required", name)
	}
	if utf8.RuneCountInString(q) > MaxQuery {
		return "", inputErrorf("%s is at most %d characters", name, MaxQuery)
	}
	return q, nil
}

// Lookup finds artifacts matching q: an entity key, a GitHub URL or owner/repo, a package
// (npm:<name>, pypi:<name>, @scope/name) or a name. kind is "", "mcp" or "skill"; limit 0
// takes the API's default. The raw answer is returned alongside the decoded one.
func (c *Client) Lookup(ctx context.Context, q, kind string, limit int) (*LookupResult, []byte, error) {
	q, err := validateQuery("q", q)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateKind(kind); err != nil {
		return nil, nil, err
	}
	params := url.Values{"q": {q}}
	if kind != "" {
		params.Set("kind", kind)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(min(limit, MaxLimit)))
	}
	var out LookupResult
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/lookup", params, nil, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// Artifact returns one artifact in full by its entity key.
func (c *Client) Artifact(ctx context.Context, key string) (*Artifact, []byte, error) {
	key, err := validateQuery("key", key)
	if err != nil {
		return nil, nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, nil, err
	}
	var out artifactEnvelope
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/artifact", url.Values{"key": {key}}, nil, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out.Artifact, raw, nil
}

// Check checks up to MaxCheck artifacts in one request. Nothing in it is stored.
func (c *Client) Check(ctx context.Context, items []CheckItem) (*CheckResult, []byte, error) {
	if len(items) == 0 {
		return nil, nil, inputErrorf("name at least one artifact to check")
	}
	if len(items) > MaxCheck {
		return nil, nil, inputErrorf("at most %d artifacts per check, got %d", MaxCheck, len(items))
	}
	clean := make([]CheckItem, len(items))
	for i, item := range items {
		q, err := validateQuery(fmt.Sprintf("artifact %d", i+1), item.Q)
		if err != nil {
			return nil, nil, err
		}
		if err := ValidateKind(item.Kind); err != nil {
			return nil, nil, err
		}
		clean[i] = CheckItem{Q: q, Kind: item.Kind}
	}
	body, err := json.Marshal(map[string]any{"artifacts": clean})
	if err != nil {
		return nil, nil, err
	}
	var out CheckResult
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/check", nil, body, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

// Status reports how much the index covers and how current it is.
func (c *Client) Status(ctx context.Context) (*Status, []byte, error) {
	var out Status
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/status", nil, nil, &out)
	if err != nil {
		return nil, nil, err
	}
	return &out, raw, nil
}

func (c *Client) do(ctx context.Context, method, path string, params url.Values, body []byte, out any) ([]byte, error) {
	target := c.BaseURL + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Beacon Intel could not be reached: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("Beacon Intel: reading the answer: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("Beacon Intel: the answer is larger than %d bytes", maxResponseBytes)
	}
	var envelope struct {
		API     string `json:"api"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	decodeErr := json.Unmarshal(raw, &envelope)
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if decodeErr == nil && envelope.API == APIVersion {
			apiErr.Code = clipLine(envelope.Error, 64)
			apiErr.Message = clipLine(envelope.Message, 300)
		}
		return nil, apiErr
	}
	if decodeErr != nil || envelope.API != APIVersion {
		return nil, fmt.Errorf("Beacon Intel: %s did not answer as the %s API", c.BaseURL, APIVersion)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("Beacon Intel: decoding the answer: %w", err)
	}
	return raw, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: requestTimeout}
}

// clipLine keeps an error message from the server to one bounded printable line.
// IsUnsafeRune reports whether r must not reach a terminal or a page from untrusted text: C0
// and C1 controls (C1 carries 8-bit CSI and OSC) and DEL, the line and paragraph separators,
// and the bidirectional formatting characters that can make one name display as another.
func IsUnsafeRune(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return true
	case r == 0x2028, r == 0x2029:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

func clipLine(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || IsUnsafeRune(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > max {
		runes := []rune(out)
		out = string(runes[:max-1]) + "…"
	}
	return out
}
