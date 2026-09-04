package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCECreatesVerifierChallengeAndState(t *testing.T) {
	p, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE returned error: %v", err)
	}
	if len(p.CodeVerifier) < 43 || len(p.State) < 43 {
		t.Fatalf("verifier/state too short: %d/%d", len(p.CodeVerifier), len(p.State))
	}
	sum := sha256.Sum256([]byte(p.CodeVerifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); p.CodeChallenge != want {
		t.Fatalf("challenge = %q, want S256 of verifier %q", p.CodeChallenge, want)
	}
	other, _ := GeneratePKCE()
	if other.State == p.State || other.CodeVerifier == p.CodeVerifier {
		t.Fatal("PKCE values must be random per call")
	}
}

func TestCallbackServerRunsExchangeOnceForAValidRedirect(t *testing.T) {
	var got []string
	exchange := func(ctx context.Context, code, state, verifier string) error {
		got = append(got, code+"|"+state+"|"+verifier)
		return nil
	}
	cs, err := NewCallbackServer("state-1", "verifier-1", exchange, 0)
	if err != nil {
		t.Fatal(err)
	}
	cs.SetSuccessPage("Beacon: device connected", "Close this tab.")
	cs.Start()
	defer func() { _ = cs.Shutdown() }()
	base := fmt.Sprintf("http://127.0.0.1:%d/callback", cs.Port())

	// Wrong state and a missing code are rejected without consuming the flow.
	for _, q := range []string{"?state=wrong&exchange_code=x", "?state=state-1"} {
		resp, err := http.Get(base + q)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "Authentication Failed") {
			t.Fatalf("expected failure page for %q, got %s", q, body)
		}
	}
	if len(got) != 0 {
		t.Fatalf("exchange must not run for invalid callbacks, ran %d times", len(got))
	}

	resp, err := http.Get(base + "?state=state-1&exchange_code=code-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Beacon: device connected") || !strings.Contains(string(body), "Close this tab.") {
		t.Fatalf("expected the custom success page, got %s", body)
	}
	result, err := cs.Wait(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.State != "state-1" {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(got) != 1 || got[0] != "code-1|state-1|verifier-1" {
		t.Fatalf("exchange calls = %v", got)
	}

	// A replayed callback is refused and does not run the exchange again.
	resp, err = http.Get(base + "?state=state-1&exchange_code=code-2")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "already handled") || len(got) != 1 {
		t.Fatalf("replay should be refused; body=%s calls=%d", body, len(got))
	}
}

func TestCallbackServerReportsExchangeFailureToBrowserAndCaller(t *testing.T) {
	exchange := func(ctx context.Context, code, state, verifier string) error {
		return errors.New("exchange code has expired; restart enrollment")
	}
	cs, err := NewCallbackServer("s", "v", exchange, 0)
	if err != nil {
		t.Fatal(err)
	}
	cs.Start()
	defer func() { _ = cs.Shutdown() }()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?state=s&exchange_code=c", cs.Port()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "exchange code has expired") {
		t.Fatalf("browser page should carry the exchange error: %s", body)
	}
	result, err := cs.Wait(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "exchange code has expired") {
		t.Fatalf("result = %#v", result)
	}
}

func TestCallbackServerErrorQueryEndsTheFlow(t *testing.T) {
	cs, err := NewCallbackServer("s", "v", func(context.Context, string, string, string) error { return nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	cs.Start()
	defer func() { _ = cs.Shutdown() }()
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?state=s&error=access_denied", cs.Port())); err != nil {
		t.Fatal(err)
	}
	result, err := cs.Wait(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "access_denied" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCallbackServerWaitTimesOut(t *testing.T) {
	cs, err := NewCallbackServer("s", "v", func(context.Context, string, string, string) error { return nil }, 0)
	if err != nil {
		t.Fatal(err)
	}
	cs.Start()
	defer func() { _ = cs.Shutdown() }()
	if _, err := cs.Wait(20 * time.Millisecond); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestCallbackServerRequiresAnExchangeFunc(t *testing.T) {
	if _, err := NewCallbackServer("s", "v", nil, 0); err == nil {
		t.Fatal("expected error for nil exchange")
	}
}

func TestPostJSONDecodesSuccessAndSurfacesServerDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "beacon-cli" {
			t.Errorf("unexpected headers %v", r.Header)
		}
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"state":"abc"}`))
		case "/detail":
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"detail":"exchange code has expired; restart enrollment"}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream down"))
		}
	}))
	defer server.Close()

	var out struct {
		State string `json:"state"`
	}
	if err := PostJSON(context.Background(), server.Client(), server.URL+"/ok", map[string]string{"a": "b"}, &out); err != nil || out.State != "abc" {
		t.Fatalf("PostJSON ok: err=%v out=%+v", err, out)
	}
	err := PostJSON(context.Background(), server.Client(), server.URL+"/detail", map[string]string{}, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusGone || err.Error() != "exchange code has expired; restart enrollment" {
		t.Fatalf("expected a 410 HTTPError with the server detail, got %v", err)
	}
	err = PostJSON(context.Background(), server.Client(), server.URL+"/plain", map[string]string{}, nil)
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadGateway || !strings.Contains(err.Error(), "upstream down") {
		t.Fatalf("expected a 502 HTTPError with the raw body, got %v", err)
	}
}

func TestResolveDashboardURL(t *testing.T) {
	t.Setenv(DashboardURLEnv, "")
	if got := ResolveDashboardURL(""); got != DefaultDashboardURL {
		t.Fatalf("default = %q", got)
	}
	if got := ResolveDashboardURL(" https://dash.example.test/ "); got != "https://dash.example.test" {
		t.Fatalf("flag = %q", got)
	}
	t.Setenv(DashboardURLEnv, "https://env.example.test/")
	if got := ResolveDashboardURL(""); got != "https://env.example.test" {
		t.Fatalf("env = %q", got)
	}
	if got := ResolveDashboardURL("https://flag.example.test"); got != "https://flag.example.test" {
		t.Fatalf("flag should win over env, got %q", got)
	}
}
