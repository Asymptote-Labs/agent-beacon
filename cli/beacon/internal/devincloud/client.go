// Package devincloud pulls Devin Cloud (autonomous agent) telemetry from the
// Devin v3 organization API and maps it into Beacon endpoint events.
//
// The autonomous Devin Cloud agent does not run the Devin CLI hook engine, so
// in-sandbox capture is not possible. Instead an org-scoped service key
// (cog_ prefix) is used from a central runner to enumerate every user's
// sessions org-wide, which is what lets an organization self-manage telemetry
// capture for all of its cloud sessions from one place.
package devincloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Devin API root.
const DefaultBaseURL = "https://api.devin.ai"

// Client is a read-only client for the Devin v3 organization API.
type Client struct {
	baseURL    string
	orgID      string
	apiKey     string
	httpClient *http.Client
	maxRetries int
	retryWait  time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (used in tests).
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if strings.TrimSpace(u) != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithRetry tunes retry behaviour (0 wait disables sleeping, for tests).
func WithRetry(maxRetries int, wait time.Duration) Option {
	return func(c *Client) {
		c.maxRetries = maxRetries
		c.retryWait = wait
	}
}

// New constructs a Client. orgID is the "org-..." id; apiKey is the cog_ key.
func New(orgID, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		orgID:      strings.TrimSpace(orgID),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		maxRetries: 3,
		retryWait:  2 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Session is a Devin org session (verified shape from GET .../sessions).
type Session struct {
	SessionID       string        `json:"session_id"`
	URL             string        `json:"url"`
	Status          string        `json:"status"`
	StatusDetail    string        `json:"status_detail"`
	Title           string        `json:"title"`
	Tags            []string      `json:"tags"`
	UserID          string        `json:"user_id"`
	OrgID           string        `json:"org_id"`
	CreatedAt       int64         `json:"created_at"`
	UpdatedAt       int64         `json:"updated_at"`
	AcusConsumed    float64       `json:"acus_consumed"`
	PullRequests    []PullRequest `json:"pull_requests"`
	ParentSessionID *string       `json:"parent_session_id"`
	ChildSessionIDs []string      `json:"child_session_ids"`
	Origin          string        `json:"origin"`
	IsArchived      bool          `json:"is_archived"`
}

// PullRequest is a pull request attached to a session. Unknown fields are
// tolerated by the JSON decoder.
type PullRequest struct {
	URL    string `json:"url"`
	Number int    `json:"number,omitempty"`
}

// Message is a single session message (GET .../sessions/{id}/messages).
type Message struct {
	EventID   string `json:"event_id"`
	Source    string `json:"source"` // "user" | "devin"
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
}

type sessionsPage struct {
	Items       []Session `json:"items"`
	EndCursor   *string   `json:"end_cursor"`
	HasNextPage bool      `json:"has_next_page"`
	Total       int       `json:"total"`
}

type messagesPage struct {
	Items       []Message `json:"items"`
	EndCursor   *string   `json:"end_cursor"`
	HasNextPage bool      `json:"has_next_page"`
}

// ListSessions returns every session visible to the org service key, following
// cursor pagination.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var all []Session
	cursor := ""
	for {
		var page sessionsPage
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		path := fmt.Sprintf("/v3/organizations/%s/sessions", url.PathEscape(c.orgID))
		if err := c.getJSON(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasNextPage || page.EndCursor == nil || *page.EndCursor == "" || *page.EndCursor == cursor {
			break
		}
		cursor = *page.EndCursor
	}
	return all, nil
}

// SessionMessages returns all messages for a session, following pagination.
func (c *Client) SessionMessages(ctx context.Context, sessionID string) ([]Message, error) {
	var all []Message
	cursor := ""
	for {
		var page messagesPage
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		path := fmt.Sprintf("/v3/organizations/%s/sessions/%s/messages", url.PathEscape(c.orgID), url.PathEscape(sessionID))
		if err := c.getJSON(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasNextPage || page.EndCursor == nil || *page.EndCursor == "" || *page.EndCursor == cursor {
			break
		}
		cursor = *page.EndCursor
	}
	return all, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out interface{}) error {
	if c.orgID == "" {
		return fmt.Errorf("devin org id is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("devin api key is required (set DEVIN_API_KEY)")
	}
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 && c.retryWait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.retryWait * time.Duration(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("devin api %s: %s", resp.Status, snippet(body))
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("devin api %s: %s", resp.Status, snippet(body))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode devin response: %w", err)
		}
		return nil
	}
	return fmt.Errorf("devin api request failed after %d attempts: %w", c.maxRetries+1, lastErr)
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
