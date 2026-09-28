package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mcpSession(baseURL string) Session {
	return Session{
		BaseURL:     baseURL,
		AccessToken: "bcn_cli_test_secret",
		TokenType:   "Bearer",
		Scopes:      []string{ScopeProfileRead, ScopeDeviceEnroll, ScopeMCPTokenCreate},
	}
}

func TestCreateMCPTokenPostsNameAndExpiryWithAccountToken(t *testing.T) {
	var authorization string
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != MCPTokenPath {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"bcn_mcp_secret","access_token":{"id":"tok-1","name":"laptop","prefix":"bcn_mcp_sec","scopes":["mcp:read"],"created_at":"2026-09-28T00:00:00Z","expires_at":"2026-10-28T00:00:00Z"},"mcp_url":"https://mcp.beacon.sh"}`))
	}))
	defer server.Close()

	token, err := CreateMCPToken(context.Background(), mcpSession(server.URL), MCPTokenRequest{Name: "  laptop ", ExpiresInDays: 30}, server.Client())
	if err != nil {
		t.Fatalf("CreateMCPToken returned error: %v", err)
	}
	if authorization != "Bearer bcn_cli_test_secret" {
		t.Fatalf("authorization = %q", authorization)
	}
	if request["name"] != "laptop" || request["expires_in_days"] != float64(30) {
		t.Fatalf("request = %#v", request)
	}
	if token.Token != "bcn_mcp_secret" || token.Info.ID != "tok-1" || token.Info.ExpiresAt == nil || token.MCPURL != "https://mcp.beacon.sh" {
		t.Fatalf("token = %#v", token)
	}
}

func TestCreateMCPTokenNeedsTheScopeBeforeSendingAnything(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()

	session := mcpSession(server.URL)
	session.Scopes = []string{ScopeProfileRead, ScopeDeviceEnroll}
	_, err := CreateMCPToken(context.Background(), session, MCPTokenRequest{Name: "laptop"}, server.Client())
	if !errors.Is(err, ErrMCPTokenScope) || called {
		t.Fatalf("err = %v called = %t", err, called)
	}
}

func TestCreateMCPTokenRejectsUnsafeInputs(t *testing.T) {
	for name, tc := range map[string]struct {
		session Session
		req     MCPTokenRequest
	}{
		"insecure url":    {mcpSession("http://beacon.example.com"), MCPTokenRequest{Name: "laptop"}},
		"empty name":      {mcpSession("https://beacon.sh"), MCPTokenRequest{Name: "  "}},
		"long name":       {mcpSession("https://beacon.sh"), MCPTokenRequest{Name: strings.Repeat("n", 101)}},
		"negative days":   {mcpSession("https://beacon.sh"), MCPTokenRequest{Name: "laptop", ExpiresInDays: -1}},
		"too many days":   {mcpSession("https://beacon.sh"), MCPTokenRequest{Name: "laptop", ExpiresInDays: 367}},
		"no access token": {Session{BaseURL: "https://beacon.sh", Scopes: []string{ScopeMCPTokenCreate}}, MCPTokenRequest{Name: "laptop"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CreateMCPToken(context.Background(), tc.session, tc.req, nil); err == nil {
				t.Fatal("CreateMCPToken accepted unsafe input")
			}
		})
	}
}

func TestCreateMCPTokenReportsServerRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"old login":   {http.StatusForbidden, `{"detail":"access token does not grant MCP token creation; run beacon login again"}`, "run `beacon login` again"},
		"bad token":   {http.StatusUnauthorized, `{"detail":"invalid access token"}`, "invalid access token"},
		"token limit": {http.StatusConflict, `{"detail":"revoke an existing token before creating another; at most 25 can be active"}`, "at most 25"},
		"no token":    {http.StatusCreated, `{"mcp_url":"https://mcp.beacon.sh"}`, "did not include a token"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := CreateMCPToken(context.Background(), mcpSession(server.URL), MCPTokenRequest{Name: "laptop"}, server.Client())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCreateMCPTokenDoesNotFollowRedirects(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	if _, err := CreateMCPToken(context.Background(), mcpSession(server.URL), MCPTokenRequest{Name: "laptop"}, server.Client()); err == nil {
		t.Fatal("CreateMCPToken succeeded on a redirect")
	}
	if leaked != "" {
		t.Fatalf("redirect target received Authorization %q", leaked)
	}
}
