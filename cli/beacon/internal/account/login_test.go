package account

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAuthService struct {
	mu       sync.Mutex
	init     map[string]any
	exchange map[string]any
	revoke   bool
	server   *httptest.Server
	expires  time.Time
}

func newFakeAuthService(t *testing.T) *fakeAuthService {
	t.Helper()
	service := &fakeAuthService{expires: time.Now().Add(time.Hour).UTC()}
	service.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &payload)
		}
		service.mu.Lock()
		defer service.mu.Unlock()
		switch r.URL.Path {
		case LoginInitPath:
			service.init = payload
			w.WriteHeader(http.StatusCreated)
		case ExchangePath:
			service.exchange = payload
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "bcn_cli_test_secret",
				"token_type":   "Bearer",
				"expires_at":   service.expires.Format(time.RFC3339),
				"user": map[string]any{
					"id": "usr_1", "email": "person@example.com", "name": "Beacon User",
				},
				"organizations": []map[string]any{{
					"id": "org_1", "slug": "acme", "name": "Acme",
				}},
				"active_organization": map[string]any{
					"id": "org_1", "slug": "acme", "name": "Acme",
				},
				"scopes": []string{"profile:read", "device:enroll"},
			})
		case RevokePath:
			if r.Header.Get("Authorization") != "Bearer bcn_cli_test_secret" {
				http.Error(w, `{"detail":"missing token"}`, http.StatusUnauthorized)
				return
			}
			service.revoke = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(service.server.Close)
	return service
}

func (s *fakeAuthService) approve(t *testing.T) func(string) error {
	t.Helper()
	return func(rawURL string) error {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		if parsed.Path != LoginPagePath {
			t.Errorf("login path = %s, want %s", parsed.Path, LoginPagePath)
		}
		if parsed.Query().Get("code_challenge") != "" {
			t.Errorf("login URL leaked code challenge: %s", rawURL)
		}
		state, port := parsed.Query().Get("state"), parsed.Query().Get("port")
		go func() {
			callbackURL := fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&exchange_code=code-123", port, state)
			resp, err := http.Get(callbackURL)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoginUsesPKCEAndReturnsPrivateSession(t *testing.T) {
	service := newFakeAuthService(t)
	now := time.Now().UTC().Truncate(time.Second)
	var out strings.Builder
	session, err := Login(context.Background(), LoginOptions{
		BaseURL:     service.server.URL,
		Version:     "v1.2.3",
		OpenBrowser: service.approve(t),
		HTTPClient:  service.server.Client(),
		Out:         &out,
		Timeout:     5 * time.Second,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if session.AccessToken != "bcn_cli_test_secret" || session.User.Email != "person@example.com" {
		t.Fatalf("session = %#v", session)
	}
	if session.ActiveOrganization == nil || session.ActiveOrganization.ID != "org_1" {
		t.Fatalf("active organization = %#v", session.ActiveOrganization)
	}
	if !session.CreatedAt.Equal(now) || session.BaseURL != service.server.URL {
		t.Fatalf("session metadata = %#v", session)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.init["code_challenge_method"] != "S256" || service.init["state"] == "" || service.init["redirect_port"] == nil {
		t.Fatalf("init payload = %#v", service.init)
	}
	client := service.init["client"].(map[string]any)
	if client["name"] != CLIClientName || client["version"] != "v1.2.3" || client["grant_version"] != CLIGrantVersion {
		t.Fatalf("client payload = %#v", client)
	}
	scopes := service.init["scopes"].([]any)
	if len(scopes) != 2 || scopes[0] != ScopeProfileRead || scopes[1] != ScopeDeviceEnroll {
		t.Fatalf("requested scopes = %#v", scopes)
	}
	if service.exchange["state"] != service.init["state"] || service.exchange["exchange_code"] != "code-123" || service.exchange["code_verifier"] == "" {
		t.Fatalf("exchange payload = %#v", service.exchange)
	}
	if !strings.Contains(out.String(), "Waiting for Beacon sign-in") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestLoginRejectsInsecureAndExpiredSessions(t *testing.T) {
	if _, err := Login(context.Background(), LoginOptions{BaseURL: "http://beacon.example.com"}); err == nil {
		t.Fatal("expected insecure URL rejection")
	}
	service := newFakeAuthService(t)
	service.expires = time.Now().Add(-time.Minute)
	_, err := Login(context.Background(), LoginOptions{
		BaseURL:     service.server.URL,
		OpenBrowser: service.approve(t),
		HTTPClient:  service.server.Client(),
		Timeout:     5 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "expired token") {
		t.Fatalf("expected expired token error, got %v", err)
	}
}

func TestRevokeUsesBearerToken(t *testing.T) {
	service := newFakeAuthService(t)
	session := Session{BaseURL: service.server.URL, AccessToken: "bcn_cli_test_secret", TokenType: "Bearer"}
	if err := Revoke(context.Background(), session, service.server.Client()); err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.revoke {
		t.Fatal("revoke endpoint was not called")
	}
}

func TestResolveBaseURLPrecedence(t *testing.T) {
	t.Setenv(BaseURLEnv, "https://env.example/")
	if got := ResolveBaseURL("https://flag.example/"); got != "https://flag.example" {
		t.Fatalf("flag URL = %q", got)
	}
	if got := ResolveBaseURL(""); got != "https://env.example" {
		t.Fatalf("env URL = %q", got)
	}
}

// Signing in to a remote machine from a local browser: the redirect to 127.0.0.1 cannot reach the
// CLI, so the person pastes the address the browser was sent to. A wrong paste is reported and
// the next line still completes the sign-in.
func TestLoginNoBrowserCompletesFromAPastedAddress(t *testing.T) {
	service := newFakeAuthService(t)
	pasteReader, pasteWriter := io.Pipe()
	defer pasteWriter.Close()

	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		_, err := Login(context.Background(), LoginOptions{
			BaseURL:   service.server.URL,
			NoBrowser: true,
			OpenBrowser: func(string) error {
				t.Error("browser must not open with NoBrowser")
				return nil
			},
			HTTPClient: service.server.Client(),
			Out:        &out,
			Timeout:    5 * time.Second,
			PasteInput: pasteReader,
		})
		done <- err
	}()

	var loginURL string
	deadline := time.Now().Add(3 * time.Second)
	for loginURL == "" && time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, service.server.URL+LoginPagePath) {
				loginURL = line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if loginURL == "" {
		t.Fatalf("login never printed its URL:\n%s", out.String())
	}
	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	state, port := parsed.Query().Get("state"), parsed.Query().Get("port")

	fmt.Fprintf(pasteWriter, "http://127.0.0.1:%s/callback?state=wrong&exchange_code=nope\n", port)
	fmt.Fprintf(pasteWriter, "http://127.0.0.1:%s/callback?state=%s&exchange_code=code-123\n", port, state)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Login: %v\n%s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Login did not finish from the pasted address:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{"paste it here", "different sign-in", "ssh -L "} {
		if !strings.Contains(text, want) {
			t.Fatalf("login output is missing %q:\n%s", want, text)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.exchange["exchange_code"] != "code-123" || service.exchange["code_verifier"] == "" {
		t.Fatalf("exchange payload = %#v", service.exchange)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
