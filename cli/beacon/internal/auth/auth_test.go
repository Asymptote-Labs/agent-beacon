package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCECreatesVerifierChallengeAndState(t *testing.T) {
	params, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE returned error: %v", err)
	}
	base64URL := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for name, value := range map[string]string{
		"code verifier":  params.CodeVerifier,
		"code challenge": params.CodeChallenge,
		"state":          params.State,
	} {
		if len(value) != 43 {
			t.Fatalf("%s length = %d, want 43", name, len(value))
		}
		if !base64URL.MatchString(value) {
			t.Fatalf("%s contains non-base64url characters: %q", name, value)
		}
	}
	hash := sha256.Sum256([]byte(params.CodeVerifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if params.CodeChallenge != wantChallenge {
		t.Fatalf("CodeChallenge = %q, want %q", params.CodeChallenge, wantChallenge)
	}
}

func TestSaveLoadCredentialsPrivatePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	creds := &Credentials{
		Token:       "secret-token",
		TokenPrefix: "asym_123",
		UserID:      "user-1",
		Email:       "user@example.test",
		OrgName:     "Example Org",
	}
	if err := SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials returned error: %v", err)
	}
	path, err := CredentialsPath()
	if err != nil {
		t.Fatalf("CredentialsPath returned error: %v", err)
	}
	if got, want := path, filepath.Join(home, ".beacon", "auth", "credentials.json"); got != want {
		t.Fatalf("CredentialsPath = %q, want %q", got, want)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat credentials dir: %v", err)
	}
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0700); got != want {
		t.Fatalf("credentials dir permissions = %o, want %o", got, want)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials file: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0600); got != want {
		t.Fatalf("credentials permissions = %o, want %o", got, want)
	}

	loaded, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials returned error: %v", err)
	}
	if loaded.Token != "secret-token" || loaded.Email != "user@example.test" {
		t.Fatalf("credentials did not round-trip: %#v", loaded)
	}
	if !IsLoggedIn() {
		t.Fatal("IsLoggedIn = false, want true")
	}
}

func TestIsLoggedInRejectsExpiredCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SaveCredentials(&Credentials{
		Token:     "expired-token",
		UserID:    "user-1",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("SaveCredentials returned error: %v", err)
	}
	if IsLoggedIn() {
		t.Fatal("IsLoggedIn = true, want false for expired credentials")
	}
}

func TestIsLoggedInRejectsIncompleteCredentials(t *testing.T) {
	for name, contents := range map[string]string{
		"missing token":   `{"token":"","user_id":"user-1"}`,
		"missing user ID": `{"token":"secret-token","user_id":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if _, err := EnsureConfigDir(); err != nil {
				t.Fatalf("EnsureConfigDir returned error: %v", err)
			}
			path, err := CredentialsPath()
			if err != nil {
				t.Fatalf("CredentialsPath returned error: %v", err)
			}
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatalf("write credentials: %v", err)
			}
			if IsLoggedIn() {
				t.Fatal("IsLoggedIn = true, want false for incomplete credentials")
			}
		})
	}
}

func TestCallbackServerExchangesCodeForToken(t *testing.T) {
	var gotRequest exchangeCodeRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/auth/exchange" {
			t.Fatalf("exchange path = %q, want /api/cli/auth/exchange", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("exchange method = %q, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode exchange request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"secret-token","token_prefix":"asym_123","user_id":"user-1","email":"user@example.test","org_id":"org-1","org_name":"Example Org"}`))
	}))
	defer backend.Close()

	server, err := NewCallbackServer("state-123", "verifier-123", backend.URL, backend.Client())
	if err != nil {
		t.Fatalf("NewCallbackServer returned error: %v", err)
	}
	defer func() { _ = server.Shutdown() }()
	server.Start()

	callbackURL := url.URL{
		Scheme:   "http",
		Host:     "127.0.0.1:" + strconv.Itoa(server.Port()),
		Path:     "/callback",
		RawQuery: "exchange_code=code-123&state=state-123",
	}
	go func() {
		resp, err := http.Get(callbackURL.String())
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	result, err := server.Wait(2 * time.Second)
	if err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("callback result error = %q", result.Error)
	}
	if result.Token != "secret-token" || result.Email != "user@example.test" || result.OrgName != "Example Org" {
		t.Fatalf("unexpected callback result: %#v", result)
	}
	if gotRequest.ExchangeCode != "code-123" || gotRequest.State != "state-123" || gotRequest.CodeVerifier != "verifier-123" {
		t.Fatalf("unexpected exchange request: %#v", gotRequest)
	}
}

func TestCallbackServerWriteTimeoutAllowsDefaultExchangeTimeout(t *testing.T) {
	server, err := NewCallbackServer("state-123", "verifier-123", "https://dashboard.example", nil)
	if err != nil {
		t.Fatalf("NewCallbackServer returned error: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	if server.httpClient.Timeout != defaultExchangeTimeout {
		t.Fatalf("default exchange timeout = %s, want %s", server.httpClient.Timeout, defaultExchangeTimeout)
	}
	if server.server.WriteTimeout <= server.httpClient.Timeout {
		t.Fatalf("callback write timeout = %s, want greater than exchange timeout %s", server.server.WriteTimeout, server.httpClient.Timeout)
	}
}

func TestLoginRejectsIncompleteExchangeResponse(t *testing.T) {
	for name, tc := range map[string]struct {
		responseBody string
		wantErr      string
	}{
		"missing token": {
			responseBody: `{"token":"","token_prefix":"asym_123","user_id":"user-1","email":"user@example.test"}`,
			wantErr:      "credentials token is required",
		},
		"missing user ID": {
			responseBody: `{"token":"secret-token","token_prefix":"asym_123","user_id":"","email":"user@example.test"}`,
			wantErr:      "credentials user ID is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.responseBody))
			}))
			defer backend.Close()

			callbackErr := make(chan error, 1)
			openBrowser := func(authURL string) error {
				u, err := url.Parse(authURL)
				if err != nil {
					return err
				}
				callbackURL := url.URL{
					Scheme:   "http",
					Host:     "127.0.0.1:" + u.Query().Get("port"),
					Path:     "/callback",
					RawQuery: "exchange_code=code-123&state=" + url.QueryEscape(u.Query().Get("state")),
				}
				go func() {
					resp, err := http.Get(callbackURL.String())
					if err == nil {
						_ = resp.Body.Close()
					}
					callbackErr <- err
				}()
				return nil
			}

			creds, err := Login(LoginOptions{
				DashboardURL: backend.URL,
				HTTPClient:   backend.Client(),
				OpenBrowser:  openBrowser,
				Timeout:      2 * time.Second,
			})
			if err == nil {
				t.Fatal("Login error = nil, want error")
			}
			if creds != nil {
				t.Fatalf("Login credentials = %#v, want nil", creds)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Login error = %q, want to contain %q", err, tc.wantErr)
			}
			if err := <-callbackErr; err != nil {
				t.Fatalf("callback request failed: %v", err)
			}
		})
	}
}

func TestCallbackServerIgnoresInvalidCallbacksBeforeValidRedirect(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		if r.URL.Path != "/api/cli/auth/exchange" {
			t.Fatalf("exchange path = %q, want /api/cli/auth/exchange", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"secret-token","token_prefix":"asym_123","user_id":"user-1","email":"user@example.test","org_id":"org-1","org_name":"Example Org"}`))
	}))
	defer backend.Close()

	server, err := NewCallbackServer("expected-state", "verifier-123", backend.URL, backend.Client())
	if err != nil {
		t.Fatalf("NewCallbackServer returned error: %v", err)
	}
	defer func() { _ = server.Shutdown() }()
	server.Start()

	baseURL := "http://127.0.0.1:" + strconv.Itoa(server.Port()) + "/callback"
	for _, rawURL := range []string{
		baseURL + "?exchange_code=code&state=wrong-state",
		baseURL + "?state=expected-state",
		baseURL + "?error=access_denied",
	} {
		resp, err := http.Get(rawURL)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls after invalid callbacks = %d, want 0", backendCalls)
	}

	resp, err := http.Get(baseURL + "?exchange_code=code-123&state=expected-state")
	if err != nil {
		t.Fatalf("valid callback request failed: %v", err)
	}
	_ = resp.Body.Close()

	result, err := server.Wait(2 * time.Second)
	if err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("callback result error = %q", result.Error)
	}
	if result.Token != "secret-token" || result.State != "expected-state" {
		t.Fatalf("unexpected callback result: %#v", result)
	}
	if backendCalls != 1 {
		t.Fatalf("backend calls = %d, want 1", backendCalls)
	}
}

func TestResolveDashboardURL(t *testing.T) {
	t.Setenv(DashboardURLEnv, "")
	if got := ResolveDashboardURL(""); got != DefaultDashboardURL {
		t.Fatalf("ResolveDashboardURL empty = %q, want default", got)
	}
	t.Setenv(DashboardURLEnv, "https://env.example/")
	if got := ResolveDashboardURL(""); got != "https://env.example" {
		t.Fatalf("ResolveDashboardURL env = %q, want env without trailing slash", got)
	}
	if got := ResolveDashboardURL("https://flag.example///"); got != "https://flag.example" {
		t.Fatalf("ResolveDashboardURL flag = %q, want flag without trailing slash", got)
	}
}
