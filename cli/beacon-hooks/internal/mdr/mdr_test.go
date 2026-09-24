package mdr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func serve(t *testing.T, status int, body string, seen *Request) Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		if seen != nil {
			_ = json.NewDecoder(r.Body).Decode(seen)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Config{URL: srv.URL, Token: "tok", Timeout: time.Second}
}

func TestConsultSendsTheRequestAndReadsADeny(t *testing.T) {
	var seen Request
	cfg := serve(t, 200, `{"decision":"deny","message":"blocked","policy_id":"p","finding_url":"https://x/f/1"}`, &seen)
	resp, err := Consult(context.Background(), cfg, Request{
		Phase: PhasePreTool, Harness: "claude", SessionID: "s",
		Tool:      &ToolCall{Name: "Bash", Input: ToolInput{Command: "pnpm config get x"}},
		Prefilter: &Prefilter{RuleIDs: []string{"secret-source.npm-config"}},
	})
	if err != nil || resp.Decision != DecisionDeny || resp.Text() != "blocked" || !resp.Flagged() {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if seen.Version != Version || seen.Phase != PhasePreTool || seen.Tool.Input.Command != "pnpm config get x" {
		t.Fatalf("request not sent as built: %+v", seen)
	}
}

func TestConsultKeepsShadowAllowDetails(t *testing.T) {
	cfg := serve(t, 200, `{"decision":"allow","mode":"monitor","policy_id":"p","reason":"would expose"}`, nil)
	resp, err := Consult(context.Background(), cfg, Request{})
	if err != nil || resp.Decision != DecisionAllow || !resp.Flagged() || resp.Mode != "monitor" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestConsultFailuresAreErrorsThatAllow(t *testing.T) {
	cases := map[string]Config{
		"status":    serve(t, 500, `{}`, nil),
		"malformed": serve(t, 200, `not json`, nil),
		"dial":      {URL: "http://127.0.0.1:1", Token: "tok", Timeout: time.Second},
	}
	for name, cfg := range cases {
		resp, err := Consult(context.Background(), cfg, Request{})
		if err == nil || resp.Decision != DecisionAllow {
			t.Errorf("%s: resp=%+v err=%v", name, resp, err)
		}
	}
	if _, err := Consult(context.Background(), Config{}, Request{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured: %v", err)
	}
}

func TestConsultTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	start := time.Now()
	_, err := Consult(context.Background(), Config{URL: srv.URL, Timeout: 50 * time.Millisecond}, Request{})
	if err == nil || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("timeout not honored: err=%v after %v", err, time.Since(start))
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		`{"decision":"DENY","message":"m"}`:  DecisionDeny,
		`{"decision":"ask","reason":"r"}`:    DecisionAsk,
		`{"decision":"deny"}`:                DecisionAllow,
		`{"decision":"maybe","message":"m"}`: DecisionAllow,
		`{}`:                                 DecisionAllow,
	} {
		var r Response
		_ = json.Unmarshal([]byte(in), &r)
		if got := normalize(r).Decision; got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestLoadConfigFromFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	_ = os.WriteFile(path, []byte(`{"url":"https://edge/v1/mdr/decide","token":"ask_live_x","timeout_ms":5000}`), 0o600)
	t.Setenv(ConfigPathEnv, path)
	t.Setenv(URLEnv, "")
	t.Setenv(TokenEnv, "")
	t.Setenv(TimeoutEnv, "")
	cfg := LoadConfig()
	if cfg.URL != "https://edge/v1/mdr/decide" || cfg.Token != "ask_live_x" || cfg.Timeout != 5*time.Second || cfg.Path != path {
		t.Fatalf("file config: %+v", cfg)
	}
	t.Setenv(URLEnv, "http://127.0.0.1:9/decide")
	if cfg := LoadConfig(); cfg.URL != "http://127.0.0.1:9/decide" || cfg.Token != "ask_live_x" {
		t.Fatalf("env override: %+v", cfg)
	}
	t.Setenv(ConfigPathEnv, filepath.Join(dir, "missing.json"))
	t.Setenv(URLEnv, "")
	if cfg := LoadConfig(); cfg.Enabled() || cfg.Timeout != DefaultTimeout {
		t.Fatalf("missing file: %+v", cfg)
	}
}
