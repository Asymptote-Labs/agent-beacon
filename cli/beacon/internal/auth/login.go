package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	DefaultDashboardURL = "https://asymptotelabs.ai"
	DashboardURLEnv     = "BEACON_DASHBOARD_URL"
	AuthTimeout         = 5 * time.Minute
)

type LoginOptions struct {
	DashboardURL string
	OpenBrowser  func(string) error
	HTTPClient   *http.Client
	Out          io.Writer
	Timeout      time.Duration
}

func Login(opts LoginOptions) (*Credentials, error) {
	dashboardURL := ResolveDashboardURL(opts.DashboardURL)
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = AuthTimeout
	}
	openBrowser := opts.OpenBrowser
	if openBrowser == nil {
		openBrowser = OpenBrowser
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}
	server, err := NewCallbackServer(pkce.State, pkce.CodeVerifier, dashboardURL, opts.HTTPClient)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = server.Shutdown()
	}()
	server.Start()

	authURL, err := buildAuthURL(dashboardURL, pkce.CodeChallenge, pkce.State, server.Port())
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Opening browser to %s/cli/auth...\n", dashboardURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintln(out, "Failed to open browser automatically.")
		fmt.Fprintf(out, "Please open this URL in your browser:\n%s\n", authURL)
	}
	fmt.Fprintln(out, "Waiting for authentication...")

	result, err := server.Wait(timeout)
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("authentication failed: %s", result.Error)
	}
	var expiresAt time.Time
	if result.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, result.ExpiresAt)
		if err != nil {
			expiresAt, err = time.Parse("2006-01-02T15:04:05", result.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("failed to parse expiration time: %w", err)
			}
		}
	}
	creds := &Credentials{
		Token:       result.Token,
		TokenPrefix: result.TokenPrefix,
		ExpiresAt:   expiresAt,
		UserID:      result.UserID,
		Email:       result.Email,
		OrgID:       result.OrgID,
		OrgName:     result.OrgName,
	}
	if err := creds.validateRequiredFields(); err != nil {
		return nil, fmt.Errorf("invalid authentication response: %w", err)
	}
	return creds, nil
}

func ResolveDashboardURL(flagValue string) string {
	if flagValue != "" {
		return normalizeDashboardURL(flagValue)
	}
	if envValue := os.Getenv(DashboardURLEnv); envValue != "" {
		return normalizeDashboardURL(envValue)
	}
	return DefaultDashboardURL
}

func buildAuthURL(dashboardURL, codeChallenge, state string, port int) (string, error) {
	u, err := url.Parse(normalizeDashboardURL(dashboardURL) + "/cli/auth")
	if err != nil {
		return "", fmt.Errorf("failed to parse dashboard URL: %w", err)
	}
	q := u.Query()
	q.Set("code_challenge", codeChallenge)
	q.Set("state", state)
	q.Set("port", fmt.Sprintf("%d", port))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func normalizeDashboardURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultDashboardURL
	}
	return strings.TrimRight(raw, "/")
}
