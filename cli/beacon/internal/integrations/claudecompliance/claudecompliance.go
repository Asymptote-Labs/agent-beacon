package claudecompliance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	Name             = "claude_compliance"
	DisplayName      = "Claude Compliance"
	DefaultBaseURL   = "https://api.anthropic.com"
	DefaultAPIKeyEnv = "ANTHROPIC_COMPLIANCE_ACCESS_KEY"

	UserStatePath   = ".beacon/integrations/claude-compliance/state.json"
	SystemStatePath = "/Library/Application Support/Beacon/Integrations/claude-compliance/state.json"

	DefaultLimit     = 100
	MaxLimit         = 5000
	DefaultMaxPages  = 1
	DefaultOverlap   = 10 * time.Minute
	DefaultRecentIDs = 10000
)

var ErrSinceRequired = errors.New("first pull requires --since to avoid importing the entire activity history")

type Client struct {
	BaseURL       string
	APIKey        string
	HTTPClient    *http.Client
	MaxRetries    int
	RetryMaxDelay time.Duration
	Sleep         func(context.Context, time.Duration) error
}

type Query struct {
	Limit           int
	Order           string
	AfterID         string
	BeforeID        string
	CreatedAtGTE    string
	CreatedAtGT     string
	CreatedAtLTE    string
	CreatedAtLT     string
	ActivityTypes   []string
	OrganizationIDs []string
	ActorIDs        []string
}

type Activity struct {
	ID               string                 `json:"id"`
	CreatedAt        string                 `json:"created_at"`
	OrganizationID   string                 `json:"organization_id"`
	OrganizationUUID string                 `json:"organization_uuid"`
	Type             string                 `json:"type"`
	Actor            map[string]interface{} `json:"actor,omitempty"`
	Raw              map[string]interface{} `json:"-"`
}

func (a *Activity) UnmarshalJSON(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias Activity
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = Activity(decoded)
	a.Raw = raw
	return nil
}

type ActivitiesResponse struct {
	Data    []Activity `json:"data"`
	HasMore bool       `json:"has_more"`
	FirstID string     `json:"first_id"`
	LastID  string     `json:"last_id"`
}

type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("claude compliance API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("claude compliance API returned HTTP %d: %s", e.StatusCode, e.Body)
}

func (c *Client) ListActivities(ctx context.Context, query Query) (ActivitiesResponse, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return ActivitiesResponse{}, fmt.Errorf("%s API key is required", DisplayName)
	}
	retries := c.MaxRetries
	if retries == 0 {
		retries = 3
	}
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		resp, retryAfter, err := c.listActivitiesOnce(ctx, query)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !retryableStatus(apiErr.StatusCode) || attempt == retries {
			return ActivitiesResponse{}, err
		}
		delay := retryAfter
		if delay <= 0 {
			delay = time.Duration(attempt+1) * time.Second
		}
		if max := c.retryMaxDelay(); max > 0 && delay > max {
			delay = max
		}
		if err := c.sleep(ctx, delay); err != nil {
			return ActivitiesResponse{}, err
		}
	}
	return ActivitiesResponse{}, lastErr
}

func (c *Client) listActivitiesOnce(ctx context.Context, query Query) (ActivitiesResponse, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.activitiesURL(query), nil)
	if err != nil {
		return ActivitiesResponse{}, 0, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return ActivitiesResponse{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ActivitiesResponse{}, parseRetryAfter(resp.Header.Get("Retry-After")), &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(bytes.TrimSpace(body)),
		}
	}
	var out ActivitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ActivitiesResponse{}, 0, err
	}
	return out, 0, nil
}

func (c *Client) activitiesURL(query Query) string {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base + "/v1/compliance/activities")
	if err != nil {
		return base + "/v1/compliance/activities"
	}
	values := u.Query()
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	values.Set("limit", strconv.Itoa(limit))
	order := strings.TrimSpace(query.Order)
	if order == "" {
		order = "asc"
	}
	values.Set("order", order)
	setIfNotEmpty(values, "after_id", query.AfterID)
	setIfNotEmpty(values, "before_id", query.BeforeID)
	setIfNotEmpty(values, "created_at.gte", query.CreatedAtGTE)
	setIfNotEmpty(values, "created_at.gt", query.CreatedAtGT)
	setIfNotEmpty(values, "created_at.lte", query.CreatedAtLTE)
	setIfNotEmpty(values, "created_at.lt", query.CreatedAtLT)
	addRepeated(values, "activity_types[]", query.ActivityTypes)
	addRepeated(values, "organization_ids[]", query.OrganizationIDs)
	addRepeated(values, "actor_ids[]", query.ActorIDs)
	u.RawQuery = values.Encode()
	return u.String()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) retryMaxDelay() time.Duration {
	if c.RetryMaxDelay > 0 {
		return c.RetryMaxDelay
	}
	return 30 * time.Second
}

func (c *Client) sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func setIfNotEmpty(values url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		values.Set(key, strings.TrimSpace(value))
	}
}

func addRepeated(values url.Values, key string, items []string) {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			values.Add(key, strings.TrimSpace(item))
		}
	}
}

type State struct {
	LastID       string   `json:"last_id,omitempty"`
	LastSyncedAt string   `json:"last_synced_at,omitempty"`
	RecentIDs    []SeenID `json:"recent_ids,omitempty"`
}

type SeenID struct {
	ID     string `json:"id"`
	SeenAt string `json:"seen_at,omitempty"`
}

func DefaultStatePath(userMode bool) string {
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", UserStatePath)
		}
		return filepath.Join(home, UserStatePath)
	}
	return SystemStatePath
}

func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func SaveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func (s State) HasSeen(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, seen := range s.RecentIDs {
		if seen.ID == id {
			return true
		}
	}
	return false
}

func (s *State) MarkSeen(id string, seenAt time.Time) {
	id = strings.TrimSpace(id)
	if id == "" || s.HasSeen(id) {
		return
	}
	s.RecentIDs = append(s.RecentIDs, SeenID{
		ID:     id,
		SeenAt: seenAt.UTC().Format(time.RFC3339),
	})
}

func (s *State) TrimRecentIDs(max int) {
	if max <= 0 {
		max = DefaultRecentIDs
	}
	if len(s.RecentIDs) <= max {
		return
	}
	s.RecentIDs = append([]SeenID(nil), s.RecentIDs[len(s.RecentIDs)-max:]...)
}

type SyncOptions struct {
	Client      *Client
	StatePath   string
	Query       Query
	MaxPages    int
	Overlap     time.Duration
	ResetCursor bool
	DryRun      bool
	WriteEvent  func(schema.Event) error
	Now         func() time.Time
}

type SyncSummary struct {
	Fetched      int    `json:"fetched"`
	Written      int    `json:"written"`
	Skipped      int    `json:"skipped"`
	Pages        int    `json:"pages"`
	StatePath    string `json:"state_path"`
	LastID       string `json:"last_id,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
}

func PullActivities(ctx context.Context, opts SyncOptions) (SyncSummary, error) {
	if opts.Client == nil {
		return SyncSummary{}, errors.New("client is required")
	}
	statePath := opts.StatePath
	if strings.TrimSpace(statePath) == "" {
		statePath = DefaultStatePath(true)
	}
	state := State{}
	if !opts.ResetCursor {
		loaded, err := LoadState(statePath)
		if err != nil {
			return SyncSummary{}, err
		}
		state = loaded
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	query := opts.Query
	query.Order = "asc"
	if query.Limit <= 0 {
		query.Limit = DefaultLimit
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	if strings.TrimSpace(state.LastID) == "" && strings.TrimSpace(query.CreatedAtGTE) == "" {
		return SyncSummary{}, ErrSinceRequired
	}
	summary := SyncSummary{StatePath: statePath, DryRun: opts.DryRun}
	originalLastID := state.LastID

	if originalLastID != "" && strings.TrimSpace(query.CreatedAtGTE) == "" {
		if since := overlapSince(state, opts.Overlap); since != "" {
			overlapQuery := query
			overlapQuery.AfterID = ""
			overlapQuery.CreatedAtGTE = since
			if err := fetchPages(ctx, opts, overlapQuery, maxPages, now, &state, &summary, false); err != nil {
				return SyncSummary{}, err
			}
		}
		query.AfterID = originalLastID
	}

	if err := fetchPages(ctx, opts, query, maxPages, now, &state, &summary, true); err != nil {
		return SyncSummary{}, err
	}
	state.LastSyncedAt = now.Format(time.RFC3339)
	state.TrimRecentIDs(DefaultRecentIDs)
	summary.LastID = state.LastID
	summary.LastSyncedAt = state.LastSyncedAt
	if !opts.DryRun {
		if err := SaveState(statePath, state); err != nil {
			return SyncSummary{}, err
		}
	}
	return summary, nil
}

func fetchPages(ctx context.Context, opts SyncOptions, query Query, maxPages int, now time.Time, state *State, summary *SyncSummary, updateCursor bool) error {
	for page := 0; page < maxPages; page++ {
		resp, err := opts.Client.ListActivities(ctx, query)
		if err != nil {
			return err
		}
		summary.Pages++
		summary.Fetched += len(resp.Data)
		for _, activity := range resp.Data {
			if state.HasSeen(activity.ID) {
				summary.Skipped++
				continue
			}
			event := EventFromActivity(activity, now)
			if !opts.DryRun {
				if opts.WriteEvent == nil {
					return errors.New("event writer is required")
				}
				if err := opts.WriteEvent(event); err != nil {
					return err
				}
			}
			state.MarkSeen(activity.ID, now)
			summary.Written++
		}
		if updateCursor && strings.TrimSpace(resp.LastID) != "" {
			state.LastID = resp.LastID
		}
		if !resp.HasMore || strings.TrimSpace(resp.LastID) == "" {
			return nil
		}
		query.AfterID = resp.LastID
	}
	return nil
}

func overlapSince(state State, overlap time.Duration) string {
	if overlap <= 0 {
		overlap = DefaultOverlap
	}
	if strings.TrimSpace(state.LastSyncedAt) == "" {
		return ""
	}
	lastSyncedAt, err := time.Parse(time.RFC3339, state.LastSyncedAt)
	if err != nil {
		return ""
	}
	return lastSyncedAt.Add(-overlap).UTC().Format(time.RFC3339)
}

func EventFromActivity(activity Activity, fallbackNow time.Time) schema.Event {
	timestamp := activity.CreatedAt
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		timestamp = fallbackNow.UTC().Format(time.RFC3339)
	}
	hostname, _ := os.Hostname()
	event := schema.Event{
		Timestamp:     timestamp,
		Vendor:        schema.Vendor,
		Product:       schema.Product,
		SchemaVersion: schema.SchemaVersion,
		Event: schema.EventInfo{
			Kind:     "compliance_activity",
			Action:   "compliance.activity." + normalizeAction(activity.Type),
			Category: "compliance",
		},
		Severity: schema.SeverityInfo,
		Endpoint: schema.EndpointInfo{
			Hostname: hostname,
			OS:       runtime.GOOS,
		},
		Harness: schema.HarnessInfo{Name: Name},
		Origin:  schema.OriginCloud,
		User: schema.UserInfo{
			Name: actorString(activity.Actor, "email_address", "unauthenticated_email_address"),
			UID:  actorString(activity.Actor, "user_id", "api_key_id", "admin_api_key_id", "service_account_id"),
		},
		Message: fmt.Sprintf("Claude Compliance activity: %s", activity.Type),
		Raw: map[string]interface{}{
			"anthropic_compliance_activity": activity.Raw,
		},
	}
	if chatID, _ := activity.Raw["claude_chat_id"].(string); chatID != "" {
		event.GenAI = &schema.GenAIInfo{Conversation: &schema.GenAIConversationInfo{ID: chatID}}
	}
	return event
}

func normalizeAction(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	lastDot := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDot = false
			continue
		}
		if !lastDot {
			b.WriteByte('.')
			lastDot = true
		}
	}
	return strings.Trim(b.String(), ".")
}

func actorString(actor map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, _ := actor[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func LastComplianceEvent(logPath string) (time.Time, bool) {
	if strings.TrimSpace(logPath) == "" {
		return time.Time{}, false
	}
	file, err := os.Open(logPath)
	if err != nil {
		return time.Time{}, false
	}
	defer file.Close()
	var last time.Time
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Harness.Name != Name {
			continue
		}
		found = true
		if parsed, err := time.Parse(time.RFC3339, event.Timestamp); err == nil && parsed.After(last) {
			last = parsed
		}
	}
	return last, found
}
