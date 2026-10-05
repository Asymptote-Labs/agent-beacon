// Package cursorusage reads Cursor's per-request token usage from the Cursor Admin API.
//
// Cursor is the one large runtime whose local surfaces carry no spend at all. No hook payload has a
// token count (preCompact reports context occupancy, which is a level, not spend), and the
// Composer records in Cursor's own state.vscdb carry no usage either. The only place Cursor
// publishes what a request cost is its Admin API, which reports every model call a team member
// made with its token counts and the amount Cursor charged for it:
//
//	POST https://api.cursor.com/teams/filtered-usage-events
//	Authorization: Basic base64(<admin API key>:)
//	{"startDate": <ms>, "endDate": <ms>, "page": 1, "pageSize": 100, "email": "...", "userId": 123}
//
// The response lists events under usageEventsDisplay (older responses: usageEvents), each with a
// millisecond timestamp, the model, a tokenUsage block (inputTokens, outputTokens,
// cacheWriteTokens, cacheReadTokens, totalCents) and the charge in chargedCents.
//
// This package is only the client. It never reads a key from anywhere itself, never writes one,
// and never puts one in an error: the caller hands it a key it got from the environment, and the
// key leaves this process only as the basic-auth header of a request to the configured Cursor
// host. Talking to the network is the explicit, user-started `beacon endpoint cursor usage sync`;
// nothing in a hook or a scheduled Beacon job calls it.
package cursorusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Cursor's Admin API host.
const DefaultBaseURL = "https://api.cursor.com"

// UsageEventsPath is the endpoint that lists per-request usage events.
const UsageEventsPath = "/teams/filtered-usage-events"

const (
	defaultPageSize = 100
	// maxPageSize is the most a page may ask for. Cursor's own dashboard pages at 100; a caller
	// asking for more gets the cap rather than a request the API may reject.
	maxPageSize = 1000
	// maxResponseBytes bounds one page's body. A page of a thousand events is tens of kilobytes,
	// so this is generous for a real response and still stops a misbehaving host from streaming
	// without end into memory.
	maxResponseBytes = 32 << 20
	// maxPages stops a paging loop that a host keeps saying has another page.
	maxPages = 10000
	// maxAttempts is how many times one page is tried. The Admin API rate-limits per team, and a
	// sync that pages through a busy week can meet a 429 partway; giving up on the first one
	// would leave every later page for the next run.
	maxAttempts = 4
	// maxRetryWait caps how long a Retry-After header can make one attempt wait.
	maxRetryWait = 60 * time.Second
)

// Client talks to the Cursor Admin API.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// UserAgent is sent on every request. Empty sends Go's default.
	UserAgent string
	// Sleep waits between retries. Nil uses a context-aware timer; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

// NewClient returns a client for Cursor's own host.
func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// TokenUsage is the token block of one usage event. TotalCents is Cursor's price for the tokens,
// which is not necessarily what was charged: a request included in a plan has a token price and
// no charge.
type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	TotalCents       *float64
}

// UsageEvent is one model request as the Admin API reports it.
type UsageEvent struct {
	Timestamp time.Time
	Model     string
	Kind      string
	MaxMode   bool
	// TokenBased reports isTokenBasedCall: whether the request was billed by tokens rather than
	// as a flat request.
	TokenBased bool
	TokenUsage TokenUsage
	// ChargedCents is what Cursor charged for the request. Nil when the response did not say.
	ChargedCents *float64
	// CursorTokenFeeCents is Cursor's own fee on top of the model price. Nil when absent.
	CursorTokenFeeCents *float64
	// RequestsCosts is the number of plan requests the call consumed. Nil when absent.
	RequestsCosts *float64
	Chargeable    *bool
	Headless      bool
	UserID        string
	UserEmail     string
}

// Query selects one window of usage events.
type Query struct {
	StartDate time.Time
	EndDate   time.Time
	// Email and UserID filter to one team member. Both empty asks for the whole team.
	Email  string
	UserID string
	// PageSize is the number of events per request. Zero means 100.
	PageSize int
}

// Page is one paginated response.
type Page struct {
	TotalCount int
	// HasNextPage is the response's own pagination.hasNextPage, when it sent one.
	HasNextPage *bool
	Events      []UsageEvent
}

// StatusError is a non-2xx response. It carries the status only: the body of an error from an
// authenticated API is not something to print, and the key is never in it.
type StatusError struct {
	StatusCode int
	Status     string
}

func (e *StatusError) Error() string {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("cursor admin API returned %s: check that the key is a Cursor Admin API key for a team admin", e.Status)
	}
	return fmt.Sprintf("cursor admin API returned %s", e.Status)
}

// ErrMissingAPIKey is returned when the client has no key.
var ErrMissingAPIKey = errors.New("missing Cursor Admin API key")

// ValidateBaseURL accepts an https URL, or plain http to a loopback address. Plain http anywhere
// else would send the admin key in the clear, so it is refused rather than warned about.
func ValidateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultBaseURL, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Cursor Admin API URL: %w", err)
	}
	if parsed.User != nil {
		return "", errors.New("cursor Admin API URL must not carry credentials")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid Cursor Admin API URL %q: no host", raw)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("cursor Admin API URL must use https (plain http is allowed only to a loopback address)")
		}
	default:
		return "", fmt.Errorf("cursor Admin API URL must use https, got %q", parsed.Scheme)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("cursor Admin API URL must not carry a query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListUsageEvents fetches one page. Page numbers start at 1.
func (c *Client) ListUsageEvents(ctx context.Context, q Query, page int) (Page, error) {
	if c == nil {
		return Page{}, errors.New("nil Cursor usage client")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return Page{}, ErrMissingAPIKey
	}
	base, err := ValidateBaseURL(c.BaseURL)
	if err != nil {
		return Page{}, err
	}
	if page <= 0 {
		page = 1
	}
	body, err := requestBody(q, page)
	if err != nil {
		return Page{}, err
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retryAfter, err := c.do(ctx, base+UsageEventsPath, body)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if retryAfter < 0 || attempt == maxAttempts {
			break
		}
		if err := c.sleep(ctx, retryAfter); err != nil {
			return Page{}, err
		}
	}
	return Page{}, lastErr
}

func requestBody(q Query, page int) ([]byte, error) {
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	body := map[string]any{
		"startDate": millis(q.StartDate),
		"endDate":   millis(q.EndDate),
		"page":      page,
		"pageSize":  pageSize,
	}
	if email := strings.TrimSpace(q.Email); email != "" {
		body["email"] = email
	}
	if raw := strings.TrimSpace(q.UserID); raw != "" {
		// The Admin API's user ids are numbers. Sending one as a string is rejected by the API
		// with a message that does not say why, so a non-numeric id is refused here instead.
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid Cursor user ID %q: Cursor user IDs are numeric", raw)
		}
		body["userId"] = id
	}
	return json.Marshal(body)
}

// do sends one request. retryAfter is how long to wait before trying again, or negative when the
// failure is not worth retrying.
func (c *Client) do(ctx context.Context, endpoint string, body []byte) (Page, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Page{}, -1, fmt.Errorf("create Cursor usage request: %w", err)
	}
	req.SetBasicAuth(c.APIKey, "")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Page{}, -1, ctx.Err()
		}
		// A transport error carries the URL, never the header, so it is safe to wrap.
		return Page{}, time.Second, fmt.Errorf("call Cursor Admin API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		statusErr := &StatusError{StatusCode: resp.StatusCode, Status: resp.Status}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return Page{}, retryDelay(resp.Header.Get("Retry-After")), statusErr
		}
		return Page{}, -1, statusErr
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Page{}, time.Second, fmt.Errorf("read Cursor usage response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return Page{}, -1, fmt.Errorf("cursor usage response exceeds %d bytes", maxResponseBytes)
	}
	page, err := DecodePage(data)
	if err != nil {
		return Page{}, -1, err
	}
	return page, 0, nil
}

func retryDelay(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 2 * time.Second
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		wait := time.Duration(seconds) * time.Second
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		return wait
	}
	if at, err := http.ParseTime(header); err == nil {
		wait := time.Until(at)
		if wait < 0 {
			wait = 0
		}
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		return wait
	}
	return 2 * time.Second
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// FetchAll pages through every event in the query's window.
func (c *Client) FetchAll(ctx context.Context, q Query) ([]UsageEvent, error) {
	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	q.PageSize = pageSize

	var out []UsageEvent
	for page := 1; page <= maxPages; page++ {
		resp, err := c.ListUsageEvents(ctx, q, page)
		if err != nil {
			return out, err
		}
		out = append(out, resp.Events...)
		if len(resp.Events) == 0 {
			return out, nil
		}
		// The response's own answer wins when it gives one. The fallbacks matter for responses
		// that send no pagination block: without them a host that ignores page numbers would be
		// asked for the same page until maxPages.
		if resp.HasNextPage != nil {
			if !*resp.HasNextPage {
				return out, nil
			}
			continue
		}
		if resp.TotalCount > 0 && page*pageSize >= resp.TotalCount {
			return out, nil
		}
		if len(resp.Events) < pageSize {
			return out, nil
		}
	}
	return out, fmt.Errorf("cursor usage paging did not finish after %d pages", maxPages)
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// --- response decoding ---

type envelope struct {
	TotalCount *flexInt        `json:"totalUsageEventsCount"`
	Pagination *pagination     `json:"pagination"`
	Display    []rawUsageEvent `json:"usageEventsDisplay"`
	Events     []rawUsageEvent `json:"usageEvents"`
}

type pagination struct {
	HasNextPage *bool `json:"hasNextPage"`
}

type rawUsageEvent struct {
	Timestamp      json.RawMessage `json:"timestamp"`
	Model          string          `json:"model"`
	Kind           string          `json:"kind"`
	KindLabel      string          `json:"kindLabel"`
	MaxMode        bool            `json:"maxMode"`
	TokenBased     bool            `json:"isTokenBasedCall"`
	TokenUsage     *rawTokenUsage  `json:"tokenUsage"`
	ChargedCents   *flexFloat      `json:"chargedCents"`
	CursorTokenFee *flexFloat      `json:"cursorTokenFee"`
	RequestsCosts  *flexFloat      `json:"requestsCosts"`
	Chargeable     *bool           `json:"isChargeable"`
	Headless       bool            `json:"isHeadless"`
	UserID         json.RawMessage `json:"userId"`
	UserEmail      string          `json:"userEmail"`
}

type rawTokenUsage struct {
	InputTokens      flexInt    `json:"inputTokens"`
	OutputTokens     flexInt    `json:"outputTokens"`
	CacheWriteTokens flexInt    `json:"cacheWriteTokens"`
	CacheReadTokens  flexInt    `json:"cacheReadTokens"`
	TotalCents       *flexFloat `json:"totalCents"`
}

// DecodePage parses one response body.
func DecodePage(data []byte) (Page, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Page{}, fmt.Errorf("decode Cursor usage response: %w", err)
	}
	raw := env.Display
	if len(raw) == 0 {
		raw = env.Events
	}
	page := Page{Events: make([]UsageEvent, 0, len(raw))}
	if env.TotalCount != nil {
		page.TotalCount = int(*env.TotalCount)
	}
	if env.Pagination != nil {
		page.HasNextPage = env.Pagination.HasNextPage
	}
	for i, item := range raw {
		ev, err := parseEvent(item)
		if err != nil {
			return Page{}, fmt.Errorf("cursor usage event %d: %w", i, err)
		}
		page.Events = append(page.Events, ev)
	}
	return page, nil
}

func parseEvent(raw rawUsageEvent) (UsageEvent, error) {
	ts, err := parseTimestamp(raw.Timestamp)
	if err != nil {
		return UsageEvent{}, err
	}
	ev := UsageEvent{
		Timestamp:  ts,
		Model:      strings.TrimSpace(raw.Model),
		Kind:       strings.TrimSpace(firstNonEmpty(raw.Kind, raw.KindLabel)),
		MaxMode:    raw.MaxMode,
		TokenBased: raw.TokenBased,
		Chargeable: raw.Chargeable,
		Headless:   raw.Headless,
		UserID:     scalarString(raw.UserID),
		UserEmail:  strings.TrimSpace(raw.UserEmail),
	}
	if raw.TokenUsage != nil {
		for name, n := range map[string]flexInt{
			"inputTokens":      raw.TokenUsage.InputTokens,
			"outputTokens":     raw.TokenUsage.OutputTokens,
			"cacheWriteTokens": raw.TokenUsage.CacheWriteTokens,
			"cacheReadTokens":  raw.TokenUsage.CacheReadTokens,
		} {
			if n < 0 {
				return UsageEvent{}, fmt.Errorf("negative %s %d", name, n)
			}
		}
		ev.TokenUsage = TokenUsage{
			InputTokens:      int64(raw.TokenUsage.InputTokens),
			OutputTokens:     int64(raw.TokenUsage.OutputTokens),
			CacheWriteTokens: int64(raw.TokenUsage.CacheWriteTokens),
			CacheReadTokens:  int64(raw.TokenUsage.CacheReadTokens),
		}
		if ev.TokenUsage.TotalCents, err = cents("tokenUsage.totalCents", raw.TokenUsage.TotalCents); err != nil {
			return UsageEvent{}, err
		}
	}
	if ev.ChargedCents, err = cents("chargedCents", raw.ChargedCents); err != nil {
		return UsageEvent{}, err
	}
	if ev.CursorTokenFeeCents, err = cents("cursorTokenFee", raw.CursorTokenFee); err != nil {
		return UsageEvent{}, err
	}
	if ev.RequestsCosts, err = cents("requestsCosts", raw.RequestsCosts); err != nil {
		return UsageEvent{}, err
	}
	return ev, nil
}

// cents validates an amount. A negative or non-finite amount is an error rather than a zero: a
// refund or a garbled number silently summed into a cost report is worse than a failed sync.
func cents(name string, value *flexFloat) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	f := float64(*value)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("non-finite %s", name)
	}
	if f < 0 {
		return nil, fmt.Errorf("negative %s %v", name, f)
	}
	return &f, nil
}

func parseTimestamp(raw json.RawMessage) (time.Time, error) {
	value := scalarString(raw)
	if value == "" {
		return time.Time{}, errors.New("missing timestamp")
	}
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ms <= 0 {
			return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
		}
		return time.UnixMilli(ms).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
}

// scalarString renders a JSON string or number as a string, and anything else as empty.
func scalarString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	return n.String()
}

// flexInt decodes a count sent as a JSON number or a numeric string. null is zero.
type flexInt int64

func (f *flexInt) UnmarshalJSON(data []byte) error {
	value := scalarString(data)
	if value == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		// Some responses write counts as floats ("12.0"). Accept them when they are whole.
		parsed, ferr := strconv.ParseFloat(value, 64)
		if ferr != nil || parsed != math.Trunc(parsed) || math.Abs(parsed) > 1<<53 {
			return fmt.Errorf("invalid token count %q", value)
		}
		n = int64(parsed)
	}
	*f = flexInt(n)
	return nil
}

// flexFloat decodes an amount sent as a JSON number or a numeric string.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	value := scalarString(data)
	if value == "" {
		return fmt.Errorf("invalid amount %s", string(data))
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("invalid amount %q", value)
	}
	*f = flexFloat(parsed)
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
