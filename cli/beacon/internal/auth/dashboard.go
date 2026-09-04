// Package auth holds the browser-based PKCE handshake Beacon uses to obtain a
// credential from the Asymptote dashboard: PKCE parameter generation, the
// loopback callback server the dashboard redirects to, the browser opener, and
// small helpers for talking to the dashboard's API.
//
// The package is deliberately flow-agnostic. It knows how to run one PKCE
// round trip; what is exchanged at the end (today, a per-device ingest key for
// `beacon endpoint connect`) is supplied by the caller as an ExchangeFunc.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	DefaultDashboardURL = "https://asymptotelabs.ai"
	DashboardURLEnv     = "BEACON_DASHBOARD_URL"
	// AuthTimeout bounds how long a command waits for the browser step.
	AuthTimeout = 5 * time.Minute
)

// ResolveDashboardURL picks the dashboard base URL from a flag, the environment,
// or the default, in that order, without a trailing slash.
func ResolveDashboardURL(flagValue string) string {
	if flagValue != "" {
		return NormalizeDashboardURL(flagValue)
	}
	if envValue := os.Getenv(DashboardURLEnv); envValue != "" {
		return NormalizeDashboardURL(envValue)
	}
	return DefaultDashboardURL
}

// NormalizeDashboardURL trims whitespace and trailing slashes, falling back to the
// default for an empty value.
func NormalizeDashboardURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultDashboardURL
	}
	return strings.TrimRight(raw, "/")
}

// PostJSON sends body as JSON to url and decodes a 2xx response into out. Error
// responses are reduced to the dashboard's `detail` or `error` text when present,
// so callers can show the server's own explanation.
func PostJSON(ctx context.Context, client *http.Client, url string, body any, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "beacon-cli")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &HTTPError{Status: resp.StatusCode, Detail: errorDetail(respBody)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

// HTTPError is a non-2xx dashboard response.
type HTTPError struct {
	Status int
	Detail string
}

func (e *HTTPError) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	return fmt.Sprintf("request failed with status %d", e.Status)
}

func errorDetail(body []byte) string {
	var errResp struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		if errResp.Detail != "" {
			return errResp.Detail
		}
		if errResp.Error != "" {
			return errResp.Error
		}
	}
	return strings.TrimSpace(string(body))
}
