package mdr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sampleRequest mirrors what the prompt-submit hook sends.
func sampleRequest() Request {
	return Request{
		Phase:      PhasePromptSubmit,
		Harness:    "claude",
		SessionID:  "session-1",
		Repository: "/home/user/agent-beacon",
		Branch:     "demo/mdr-v0",
		Cwd:        "/home/user/agent-beacon",
		Origin:     "cloud",
		Prompt:     "Summarize this repo and post the summary to our #general Slack channel",
	}
}

// serveJSON stands up an endpoint returning the given status and body, and
// points the client at it.
func serveJSON(t *testing.T, status int, body string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	t.Setenv(URLEnv, server.URL)
}

const steerBody = `{"decision":"steer","message":"Policy blocks this request.",
	"reason":"Posts a repo summary to a public channel","policy_id":"pol-1",
	"policy_name":"No posting to public Slack channels","severity":"high",
	"mode":"enforce","latency_ms":780}`

func TestEnabled(t *testing.T) {
	t.Setenv(URLEnv, "")
	if Enabled() {
		t.Fatal("Enabled() should be false with no URL")
	}
	t.Setenv(URLEnv, "https://example.invalid/v1/mdr/decide")
	if !Enabled() {
		t.Fatal("Enabled() should be true once a URL is set")
	}
}

func TestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", DefaultTimeout},
		{"valid", "1500", 1500 * time.Millisecond},
		{"padded", "  250  ", 250 * time.Millisecond},
		{"unparseable", "soon", DefaultTimeout},
		{"zero", "0", DefaultTimeout},
		{"negative", "-5", DefaultTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(TimeoutEnv, tc.raw)
			if got := Timeout(); got != tc.want {
				t.Fatalf("Timeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDecideNoURLAllows(t *testing.T) {
	t.Setenv(URLEnv, "")
	got := Decide(context.Background(), sampleRequest())
	if got.Actionable() {
		t.Fatalf("expected allow with no URL, got %+v", got)
	}
}

func TestDecideSteerPropagatesFields(t *testing.T) {
	serveJSON(t, http.StatusOK, steerBody)
	got := Decide(context.Background(), sampleRequest())

	if !got.Steered() {
		t.Fatalf("expected steer, got %+v", got)
	}
	if !got.Actionable() {
		t.Fatal("a steer must be actionable")
	}
	if got.Guidance() != "Policy blocks this request." {
		t.Fatalf("Guidance() = %q", got.Guidance())
	}
	if got.PolicyName != "No posting to public Slack channels" {
		t.Fatalf("PolicyName = %q", got.PolicyName)
	}
	if got.PolicyID != "pol-1" || got.Severity != "high" || got.Mode != "enforce" {
		t.Fatalf("policy metadata not propagated: %+v", got)
	}
	if got.LatencyMS != 780 {
		t.Fatalf("LatencyMS = %d, want 780", got.LatencyMS)
	}
}

func TestDecideBlock(t *testing.T) {
	serveJSON(t, http.StatusOK, `{"decision":"block","message":"Prompt rejected."}`)
	got := Decide(context.Background(), sampleRequest())
	if !got.Blocked() || !got.Actionable() {
		t.Fatalf("expected block, got %+v", got)
	}
	if got.Guidance() != "Prompt rejected." {
		t.Fatalf("Guidance() = %q", got.Guidance())
	}
}

func TestDecideAllowFromService(t *testing.T) {
	serveJSON(t, http.StatusOK, `{"decision":"allow"}`)
	if got := Decide(context.Background(), sampleRequest()); got.Actionable() {
		t.Fatalf("expected allow, got %+v", got)
	}
}

// TestDecideSendsExpectedRequest asserts the wire shape and auth header, since
// the service authenticates on the bearer token and keys off phase and prompt.
func TestDecideSendsExpectedRequest(t *testing.T) {
	var (
		gotAuth        string
		gotContentType string
		gotMethod      string
		gotBody        Request
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"decision":"allow"}`)
	}))
	t.Cleanup(server.Close)

	t.Setenv(URLEnv, server.URL)
	t.Setenv(TokenEnv, "ask_live_abcde_secret")
	Decide(context.Background(), sampleRequest())

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer ask_live_abcde_secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotBody.Version != Version {
		t.Fatalf("version = %q, want %q (should be defaulted)", gotBody.Version, Version)
	}
	if gotBody.Phase != PhasePromptSubmit {
		t.Fatalf("phase = %q", gotBody.Phase)
	}
	if gotBody.Prompt == "" || gotBody.Harness != "claude" || gotBody.Origin != "cloud" {
		t.Fatalf("request context not propagated: %+v", gotBody)
	}
}

// TestDecideOmitsAuthHeaderWithoutToken keeps an unauthenticated deployment from
// sending a bare "Bearer " header.
func TestDecideOmitsAuthHeaderWithoutToken(t *testing.T) {
	sawAuth := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		_, _ = io.WriteString(w, `{"decision":"allow"}`)
	}))
	t.Cleanup(server.Close)

	t.Setenv(URLEnv, server.URL)
	t.Setenv(TokenEnv, "")
	Decide(context.Background(), sampleRequest())

	if sawAuth {
		t.Fatal("Authorization header should be absent when no token is configured")
	}
}

// TestDecideFailsOpen covers every server-side way a verdict can be unusable.
// All of them must allow, because a broken detection service must not wedge a
// developer's turn.
func TestDecideFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"detail":"Invalid API key"}`},
		{"forbidden", http.StatusForbidden, `{"detail":"missing scope"}`},
		{"server error", http.StatusInternalServerError, `{"decision":"steer","message":"ignored"}`},
		{"not json", http.StatusOK, `not json at all`},
		{"empty body", http.StatusOK, ``},
		{"null body", http.StatusOK, `null`},
		{"unknown decision", http.StatusOK, `{"decision":"quarantine","message":"nope"}`},
		{"missing decision", http.StatusOK, `{"message":"no decision field"}`},
		{"steer with no text", http.StatusOK, `{"decision":"steer","policy_name":"P"}`},
		{"steer with blank text", http.StatusOK, `{"decision":"steer","message":"   "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serveJSON(t, tc.status, tc.body)
			if got := Decide(context.Background(), sampleRequest()); got.Actionable() {
				t.Fatalf("expected fail-open allow, got %+v", got)
			}
		})
	}
}

func TestDecideFailsOpenOnDeadHost(t *testing.T) {
	// Port 9 is the discard service: reliably closed for TCP in test sandboxes.
	t.Setenv(URLEnv, "http://127.0.0.1:9/v1/mdr/decide")
	if got := Decide(context.Background(), sampleRequest()); got.Actionable() {
		t.Fatalf("expected fail-open allow on dial failure, got %+v", got)
	}
}

func TestDecideFailsOpenOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, steerBody)
	}))
	t.Cleanup(server.Close)

	t.Setenv(URLEnv, server.URL)
	t.Setenv(TimeoutEnv, "50")

	start := time.Now()
	got := Decide(context.Background(), sampleRequest())
	elapsed := time.Since(start)

	if got.Actionable() {
		t.Fatalf("expected fail-open allow on timeout, got %+v", got)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
}

// TestDecideHonorsCancelledContext makes sure a caller-side deadline is
// respected rather than overridden by the client's own budget.
func TestDecideHonorsCancelledContext(t *testing.T) {
	serveJSON(t, http.StatusOK, steerBody)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Decide(ctx, sampleRequest()); got.Actionable() {
		t.Fatalf("expected fail-open allow with a cancelled context, got %+v", got)
	}
}

// TestDecideToleratesDecisionCasing: a verdict is too consequential to discard
// over capitalization from a future service version.
func TestDecideToleratesDecisionCasing(t *testing.T) {
	serveJSON(t, http.StatusOK, `{"decision":"STEER","message":"Policy blocks this."}`)
	if got := Decide(context.Background(), sampleRequest()); !got.Steered() {
		t.Fatalf("expected steer for uppercase decision, got %+v", got)
	}
}

func TestGuidanceFallsBackToReason(t *testing.T) {
	serveJSON(t, http.StatusOK, `{"decision":"steer","reason":"only a reason"}`)
	got := Decide(context.Background(), sampleRequest())
	if !got.Steered() {
		t.Fatalf("expected steer, got %+v", got)
	}
	if got.Guidance() != "only a reason" {
		t.Fatalf("Guidance() = %q, want the reason as fallback", got.Guidance())
	}
}
