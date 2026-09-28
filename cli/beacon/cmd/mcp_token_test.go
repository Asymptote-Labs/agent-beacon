package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
)

func withMCPTokenFakes(t *testing.T, session *account.Session, loadErr error) *account.MCPTokenRequest {
	t.Helper()
	origLoad, origCreate, origHost, origOpts := mcpTokenAccountLoad, mcpTokenCreate, mcpTokenHostname, mcpTokenOpts
	t.Cleanup(func() {
		mcpTokenAccountLoad, mcpTokenCreate, mcpTokenHostname, mcpTokenOpts = origLoad, origCreate, origHost, origOpts
	})
	mcpTokenOpts.name, mcpTokenOpts.expiresInDays, mcpTokenOpts.json = "", defaultMCPTokenExpiryDays, false
	mcpTokenAccountLoad = func() (*account.Session, error) { return session, loadErr }
	mcpTokenHostname = func() (string, error) { return "mac.local", nil }
	sent := &account.MCPTokenRequest{}
	mcpTokenCreate = func(_ context.Context, s account.Session, req account.MCPTokenRequest, _ *http.Client) (*account.MCPToken, error) {
		*sent = req
		return &account.MCPToken{Token: "bcn_mcp_secret", MCPURL: "https://mcp.beacon.sh", Info: account.MCPTokenInfo{Name: req.Name, Prefix: "bcn_mcp_sec"}}, nil
	}
	return sent
}

func runMCPTokenCreateForTest(t *testing.T) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runMCPTokenCreate(cmd, nil)
	return stdout.String(), stderr.String(), err
}

func signedInMCPSession() *account.Session {
	return &account.Session{
		BaseURL: "https://beacon.sh", AccessToken: "never-print-this-token", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		Scopes:    []string{account.ScopeProfileRead, account.ScopeDeviceEnroll, account.ScopeMCPTokenCreate},
	}
}

func TestMCPTokenCreatePrintsOnlyTheTokenOnStdout(t *testing.T) {
	sent := withMCPTokenFakes(t, signedInMCPSession(), nil)
	stdout, stderr, err := runMCPTokenCreateForTest(t)
	if err != nil {
		t.Fatalf("runMCPTokenCreate: %v", err)
	}
	if stdout != "bcn_mcp_secret\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stderr, "bcn_mcp_secret") || strings.Contains(stdout+stderr, "never-print-this-token") {
		t.Fatalf("stderr leaked a secret: %q", stderr)
	}
	if sent.Name != "beacon-cli on mac.local" || sent.ExpiresInDays != defaultMCPTokenExpiryDays {
		t.Fatalf("request = %#v", sent)
	}
}

func TestMCPTokenCreateJSON(t *testing.T) {
	withMCPTokenFakes(t, signedInMCPSession(), nil)
	mcpTokenOpts.json = true
	stdout, _, err := runMCPTokenCreateForTest(t)
	if err != nil {
		t.Fatalf("runMCPTokenCreate: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out["token"] != "bcn_mcp_secret" || out["mcp_url"] != "https://mcp.beacon.sh" {
		t.Fatalf("stdout = %q err = %v", stdout, err)
	}
	if strings.Contains(stdout, "never-print-this-token") {
		t.Fatal("JSON leaked the account token")
	}
}

func TestMCPTokenCreateRefusesBeforeCalling(t *testing.T) {
	expired := signedInMCPSession()
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	for name, tc := range map[string]struct {
		session *account.Session
		loadErr error
		days    int
		want    string
	}{
		"not signed in": {nil, account.ErrNotSignedIn, defaultMCPTokenExpiryDays, "beacon login"},
		"expired":       {expired, nil, defaultMCPTokenExpiryDays, "expired"},
		"bad expiry":    {signedInMCPSession(), nil, 400, "between 0"},
	} {
		t.Run(name, func(t *testing.T) {
			withMCPTokenFakes(t, tc.session, tc.loadErr)
			mcpTokenOpts.expiresInDays = tc.days
			called := false
			mcpTokenCreate = func(context.Context, account.Session, account.MCPTokenRequest, *http.Client) (*account.MCPToken, error) {
				called = true
				return nil, nil
			}
			stdout, _, err := runMCPTokenCreateForTest(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) || called || stdout != "" {
				t.Fatalf("err = %v called = %t stdout = %q", err, called, stdout)
			}
		})
	}
}
