package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	MCPTokenPath = "/api/cli/mcp/tokens"
	// MaxMCPTokenExpiryDays is the longest lifetime beacon.sh accepts; 0 means the token never
	// expires.
	MaxMCPTokenExpiryDays = 366
	MaxMCPTokenNameRunes  = 100
	maxMCPTokenNameLength = MaxMCPTokenNameRunes
	mcpTokenPrefix        = "bcn_mcp_"
)

// ErrMCPTokenScope means the signed-in session cannot mint MCP tokens: it was issued before
// `beacon login` asked for ScopeMCPTokenCreate, or beacon.sh refused that scope.
var ErrMCPTokenScope = errors.New("this Beacon sign-in cannot create MCP tokens: run `beacon login` again")

type MCPTokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// MCPTokenInfo describes a created token. It never holds the token itself.
type MCPTokenInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// MCPToken is the response of MCPTokenPath: the same shape as the dashboard's MCP token
// creation. Token is shown once and cannot be retrieved again.
type MCPToken struct {
	Token  string       `json:"token"`
	Info   MCPTokenInfo `json:"access_token"`
	MCPURL string       `json:"mcp_url"`
}

// ValidateMCPTokenRequest applies beacon.sh's limits locally, so a bad name or lifetime is
// reported without sending the account token anywhere.
func ValidateMCPTokenRequest(req MCPTokenRequest) error {
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		return errors.New("an MCP token needs a name")
	case len([]rune(name)) > maxMCPTokenNameLength:
		return fmt.Errorf("an MCP token name can be at most %d characters", maxMCPTokenNameLength)
	case strings.ContainsRune(name, 0):
		return errors.New("an MCP token name cannot contain NUL")
	case req.ExpiresInDays < 0 || req.ExpiresInDays > MaxMCPTokenExpiryDays:
		return fmt.Errorf("expiry must be between 0 (never) and %d days", MaxMCPTokenExpiryDays)
	}
	return nil
}

// CreateMCPToken mints a personal Beacon Managed MCP token for the signed-in user. The account
// token authorizes the request and is sent only to the session's own auth service.
func CreateMCPToken(ctx context.Context, session Session, req MCPTokenRequest, client *http.Client) (*MCPToken, error) {
	req.Name = strings.TrimSpace(req.Name)
	if err := ValidateMCPTokenRequest(req); err != nil {
		return nil, err
	}
	if !secureURL(session.BaseURL) {
		return nil, errors.New("refusing to send the Beacon credential to an insecure URL")
	}
	if session.AccessToken == "" {
		return nil, ErrNotSignedIn
	}
	if !hasScope(session.Scopes, ScopeMCPTokenCreate) {
		return nil, ErrMCPTokenScope
	}
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	// A redirect would resend the account token somewhere else.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(session.BaseURL, "/")+MCPTokenPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", firstNonEmpty(session.TokenType, "Bearer")+" "+session.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", CLIClientName)
	resp, err := clientCopy.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("create MCP token: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("create MCP token: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var failure struct {
			Detail string `json:"detail"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		detail := firstNonEmpty(failure.Detail, failure.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("Beacon rejected the sign-in (%s): run `beacon login` again", detail)
		case http.StatusForbidden:
			return nil, ErrMCPTokenScope
		}
		return nil, fmt.Errorf("create MCP token: %s", detail)
	}
	var token MCPToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return nil, fmt.Errorf("parse MCP token response: %w", err)
	}
	if !strings.HasPrefix(token.Token, mcpTokenPrefix) {
		return nil, errors.New("MCP token response did not include a token")
	}
	return &token, nil
}

func hasScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}
