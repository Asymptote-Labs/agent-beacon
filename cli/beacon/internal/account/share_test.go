package account

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const shareTestToken = "bcn_cli_share_secret"

// The share client reads BEACON_API_URL. A developer's own setting must not send these tests'
// requests anywhere but their test servers.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(APIURLEnv)
	os.Exit(m.Run())
}

func shareCred(baseURL string) ShareCredential {
	return ShareCredential{BaseURL: baseURL, Token: shareTestToken, Source: CredentialSourceLogin, User: User{ID: "u", Email: "dev@example.com"}}
}

func TestResolveShareCredentialPrecedence(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	good := &Session{BaseURL: "https://beacon.sh", AccessToken: "bcn_cli_x", Scopes: []string{ScopeTracesWrite}, User: User{ID: "u", Email: "e@x"}, ExpiresAt: now.Add(time.Hour)}
	stale := &Session{BaseURL: "https://beacon.sh", AccessToken: "bcn_cli_x", Scopes: []string{ScopeProfileRead}, User: User{ID: "u", Email: "e@x"}}
	expired := &Session{BaseURL: "https://beacon.sh", AccessToken: "bcn_cli_x", Scopes: []string{ScopeTracesWrite}, User: User{ID: "u", Email: "e@x"}, ExpiresAt: now.Add(-time.Hour)}
	load := func(s *Session, err error) func() (*Session, error) {
		return func() (*Session, error) { return s, err }
	}
	env := func(value string) func(string) string {
		return func(name string) string {
			if name == APIKeyEnv {
				return value
			}
			return ""
		}
	}
	cases := []struct {
		name       string
		opts       ResolveShareCredentialOptions
		wantSource string
		wantErr    error
		errText    string
	}{
		{"flag beats login", ResolveShareCredentialOptions{Key: "bcn_api_flag", Load: load(good, nil), Getenv: env("bcn_api_env")}, CredentialSourceFlag, nil, ""},
		{"login beats env", ResolveShareCredentialOptions{Load: load(good, nil), Getenv: env("bcn_api_env")}, CredentialSourceLogin, nil, ""},
		{"env when signed out", ResolveShareCredentialOptions{Load: load(nil, ErrNotSignedIn), Getenv: env("bcn_api_env")}, CredentialSourceEnv, nil, ""},
		{"env when the login lacks the scope", ResolveShareCredentialOptions{Load: load(stale, nil), Getenv: env("bcn_api_env")}, CredentialSourceEnv, nil, ""},
		{"older login without env", ResolveShareCredentialOptions{Load: load(stale, nil), Getenv: env("")}, "", ErrTracesWriteScope, ""},
		{"expired login", ResolveShareCredentialOptions{Load: load(expired, nil)}, "", nil, "expired"},
		{"nothing", ResolveShareCredentialOptions{Load: load(nil, ErrNotSignedIn)}, "", ErrNoShareCredential, ""},
		{"bad key", ResolveShareCredentialOptions{Key: "sk-something", Load: load(good, nil)}, "", nil, "not a Beacon API key"},
		{"login for the named service", ResolveShareCredentialOptions{BaseURL: "https://beacon.sh/", Load: load(good, nil)}, CredentialSourceLogin, nil, ""},
		{"login for another service is not used", ResolveShareCredentialOptions{BaseURL: "https://staging.beacon.sh", Load: load(good, nil)}, "", nil, "signed in to https://beacon.sh, not https://staging.beacon.sh"},
		{"env key for another service", ResolveShareCredentialOptions{BaseURL: "https://staging.beacon.sh", Load: load(good, nil), Getenv: env("bcn_api_env")}, CredentialSourceEnv, nil, ""},
		{"service named by the environment", ResolveShareCredentialOptions{Load: load(good, nil), Getenv: func(name string) string {
			if name == BaseURLEnv {
				return "https://staging.beacon.sh"
			}
			return ""
		}}, "", nil, "not https://staging.beacon.sh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Now = now
			cred, err := ResolveShareCredential(tc.opts)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			case tc.errText != "":
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want %q", err, tc.errText)
				}
				if strings.Contains(err.Error(), "sk-something") {
					t.Fatalf("the rejected key was echoed: %v", err)
				}
				return
			case err != nil:
				t.Fatalf("err = %v", err)
			}
			if cred.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", cred.Source, tc.wantSource)
			}
			if strings.Contains(fmt.Sprintf("%v %+v %#v %s", cred, cred, cred, cred), cred.Token) {
				t.Fatal("formatting a credential prints its token")
			}
		})
	}
}

func TestShareClientRefusesInsecureURL(t *testing.T) {
	if _, err := NewShareClient(ShareCredential{BaseURL: "http://beacon.example.com", Token: "x"}, nil); err == nil {
		t.Fatal("expected an error for a non-loopback http URL")
	}
}

type shareServer struct {
	mu       sync.Mutex
	server   *httptest.Server
	auth     []string
	batches  [][]asymptoteobserve.TraceEventV1
	encoding []string
	trace    *asymptoteobserve.ShareTraceV1
	paths    []string
}

func newShareServer(t *testing.T) *shareServer {
	s := &shareServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.paths = append(s.paths, r.Method+" "+r.URL.EscapedPath())
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/traces/claude_code:abc":
			var trace asymptoteobserve.ShareTraceV1
			if err := json.NewDecoder(r.Body).Decode(&trace); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			s.trace = &trace
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"k3x","external_id":"claude_code:abc","url":"https://beacon.sh/s/k3x","visibility":"direct","event_count":0,"created":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/traces/claude_code:abc/events/batch":
			s.encoding = append(s.encoding, r.Header.Get("Content-Encoding"))
			var body io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" {
				zr, err := gzip.NewReader(r.Body)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				body = zr
			}
			var batch asymptoteobserve.ShareEventsBatchV1
			if err := json.NewDecoder(body).Decode(&batch); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			s.batches = append(s.batches, batch.Events)
			total := 0
			for _, b := range s.batches {
				total += len(b)
			}
			fmt.Fprintf(w, `{"upserted":%d,"event_count":%d}`, len(batch.Events), total)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/traces/claude_code:abc":
			fmt.Fprint(w, `{"id":"k3x","url":"https://beacon.sh/s/k3x","visibility":"direct","event_count":2,"last_event_id":"e2","max_number":2}`)
		default:
			http.Error(w, `{"detail":"no such trace"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func eventsN(n int, textBytes int) []asymptoteobserve.TraceEventV1 {
	out := make([]asymptoteobserve.TraceEventV1, n)
	for i := range out {
		out[i] = asymptoteobserve.TraceEventV1{
			ID: fmt.Sprintf("e%d", i+1), Number: i + 1, Type: "user_message", Action: "prompt.submitted", Timestamp: "2026-10-01T00:00:00Z",
			Content: &asymptoteobserve.TraceContentV1{Text: strings.Repeat("x", textBytes), Retention: "full", Included: true},
		}
	}
	return out
}

func shareTrace(visibility string) asymptoteobserve.ShareTraceV1 {
	return asymptoteobserve.ShareTraceV1{Summary: asymptoteobserve.TraceSummaryV1{Harness: asymptoteobserve.TraceHarnessV1{Name: "claude_code"}}, Visibility: visibility}
}

func TestShareClientPutGetAndBatches(t *testing.T) {
	s := newShareServer(t)
	client, err := NewShareClient(shareCred(s.server.URL), s.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := client.PutTrace(ctx, "claude_code:abc", shareTrace("direct"))
	if err != nil {
		t.Fatalf("PutTrace: %v", err)
	}
	if result.ID != "k3x" || !result.Created || s.trace.SchemaVersion != asymptoteobserve.TraceSchemaVersion {
		t.Fatalf("result = %+v trace = %+v", result, s.trace)
	}

	put, err := client.PostEvents(ctx, "claude_code:abc", asymptoteobserve.ShareEventsBatchV1{Events: eventsN(2500, 10)})
	if err != nil {
		t.Fatalf("PostEvents: %v", err)
	}
	if put.Batches != 3 || put.Upserted != 2500 || put.EventCount != 2500 {
		t.Fatalf("put = %+v", put)
	}
	if len(s.batches[0]) != 1000 || len(s.batches[2]) != 500 || s.batches[2][499].ID != "e2500" {
		t.Fatalf("batch sizes = %d %d %d", len(s.batches[0]), len(s.batches[1]), len(s.batches[2]))
	}
	for _, encoding := range s.encoding {
		if encoding != "gzip" {
			t.Fatalf("batch sent with Content-Encoding %q", encoding)
		}
	}

	state, found, err := client.GetTrace(ctx, "claude_code:abc")
	if err != nil || !found || state.LastEventID != "e2" || state.MaxNumber != 2 {
		t.Fatalf("GetTrace = %+v %v %v", state, found, err)
	}
	if _, found, err := client.GetTrace(ctx, "claude_code:missing"); err != nil || found {
		t.Fatalf("missing trace: found=%v err=%v", found, err)
	}
	for _, auth := range s.auth {
		if auth != "Bearer "+shareTestToken {
			t.Fatalf("authorization = %q", auth)
		}
	}
	if _, err := client.PostEvents(ctx, "bad id!", asymptoteobserve.ShareEventsBatchV1{Events: eventsN(1, 1)}); err == nil {
		t.Fatal("expected an invalid external id to be refused")
	}
}

func TestShareBatchesHonorTheByteLimit(t *testing.T) {
	batches, err := ShareBatches(eventsN(10, 1000), 1000, 3500)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, b := range batches {
		if len(b.Body) > 3500 {
			t.Fatalf("batch of %d bytes exceeds the limit", len(b.Body))
		}
		var decoded asymptoteobserve.ShareEventsBatchV1
		if err := json.Unmarshal(b.Body, &decoded); err != nil || len(decoded.Events) != b.Events {
			t.Fatalf("batch does not decode: %v", err)
		}
		total += b.Events
	}
	if total != 10 || len(batches) < 4 {
		t.Fatalf("batches = %d, events = %d", len(batches), total)
	}
	if _, err := ShareBatches(eventsN(1, 5000), 1000, 3500); err == nil {
		t.Fatal("expected an oversized event to be refused")
	}
	if batches, err := ShareBatches(nil, 1000, 3500); err != nil || len(batches) != 0 {
		t.Fatalf("empty input = %v %v", batches, err)
	}
}

func TestShareClientRefusesRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := NewShareClient(shareCred(redirect.URL), redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PutTrace(context.Background(), "claude_code:abc", shareTrace(""))
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v", err)
	}
	if followed {
		t.Fatal("the redirect was followed with the credential")
	}
}

func TestShareClientMapsAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		source string
		want   string
		is     error
	}{
		{http.StatusUnauthorized, CredentialSourceLogin, "run `beacon login` again", nil},
		{http.StatusForbidden, CredentialSourceLogin, "", ErrTracesWriteScope},
		{http.StatusUnauthorized, CredentialSourceFlag, "rejected the API key from --key", nil},
		{http.StatusForbidden, CredentialSourceEnv, "needs the traces:write scope", nil},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			// A server echoing the credential back must not get it into the error.
			fmt.Fprintf(w, `{"detail":"bad token %s"}`, shareTestToken)
		}))
		cred := shareCred(server.URL)
		cred.Source = tc.source
		client, err := NewShareClient(cred, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.PutTrace(context.Background(), "claude_code:abc", shareTrace(""))
		server.Close()
		if err == nil {
			t.Fatalf("%d/%s: expected an error", tc.status, tc.source)
		}
		if tc.is != nil && !errors.Is(err, tc.is) {
			t.Fatalf("%d/%s: err = %v, want %v", tc.status, tc.source, err, tc.is)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%d/%s: err = %v, want %q", tc.status, tc.source, err, tc.want)
		}
		if strings.Contains(err.Error(), shareTestToken) {
			t.Fatalf("%d/%s: the token reached the error: %v", tc.status, tc.source, err)
		}
	}
}

// beacon.sh serves its web app, not /v1: a sign-in there shares through api.beacon.sh. Other
// services keep their own base URL, and BEACON_API_URL names the API outright.
func TestShareAPIBaseURL(t *testing.T) {
	for _, tc := range []struct{ base, env, want string }{
		{"https://beacon.sh", "", DefaultAPIURL},
		{"https://beacon.sh/", "", DefaultAPIURL},
		{"https://www.beacon.sh", "", DefaultAPIURL},
		{"https://BEACON.SH", "", DefaultAPIURL},
		{"https://beacon.example.com", "", "https://beacon.example.com"},
		{"http://127.0.0.1:8787/", "", "http://127.0.0.1:8787"},
		{"https://beacon.sh", " https://api.staging.example.com/ ", "https://api.staging.example.com"},
		{"http://127.0.0.1:8787", "https://api.example.com", "https://api.example.com"},
	} {
		getenv := func(name string) string {
			if name == APIURLEnv {
				return tc.env
			}
			return ""
		}
		if got := ShareAPIBaseURL(tc.base, getenv); got != tc.want {
			t.Errorf("ShareAPIBaseURL(%q, %s=%q) = %q, want %q", tc.base, APIURLEnv, tc.env, got, tc.want)
		}
	}
	if got := ShareAPIBaseURL("https://beacon.sh", nil); got != DefaultAPIURL {
		t.Errorf("ShareAPIBaseURL with no environment = %q", got)
	}
}

type shareRoundTripFunc func(*http.Request) (*http.Response, error)

func (f shareRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestShareClientSendsABeaconShSignInToTheAPIHost(t *testing.T) {
	var requested []string
	httpClient := &http.Client{Transport: shareRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requested = append(requested, r.Method+" "+r.URL.String())
		body := `{"id":"k3x","url":"https://beacon.sh/s/k3x","visibility":"direct","event_count":0,"last_event_id":"","max_number":0}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	client, err := NewShareClient(shareCred("https://beacon.sh"), httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL() != DefaultAPIURL {
		t.Fatalf("BaseURL = %q, want %q", client.BaseURL(), DefaultAPIURL)
	}
	if _, found, err := client.GetTrace(context.Background(), "claude_code:abc"); err != nil || !found {
		t.Fatalf("GetTrace: found=%v err=%v", found, err)
	}
	if len(requested) != 1 || requested[0] != "GET https://api.beacon.sh/v1/traces/claude_code:abc" {
		t.Fatalf("requests = %v", requested)
	}
}

func TestShareClientTakesTheAPIFromTheEnvironment(t *testing.T) {
	s := newShareServer(t)
	t.Setenv(APIURLEnv, s.server.URL+"/")
	client, err := NewShareClient(shareCred("https://beacon.sh"), s.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.GetTrace(context.Background(), "claude_code:abc"); err != nil || !found {
		t.Fatalf("GetTrace: found=%v err=%v", found, err)
	}
	if len(s.paths) != 1 || s.paths[0] != "GET /v1/traces/claude_code:abc" {
		t.Fatalf("requests = %v", s.paths)
	}

	t.Setenv(APIURLEnv, "http://api.example.com")
	if _, err := NewShareClient(shareCred("https://beacon.sh"), nil); err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("an http %s was accepted: %v", APIURLEnv, err)
	}
}

// A host with no trace API answers with its own HTML 404 page. That is not "the trace does not exist
// yet": GetTrace reports it, so the caller never goes on to create the trace there.
func TestShareClientReportsAHostWithNoTraceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<!DOCTYPE html><html><head><title>404: This page could not be found.</title></head><body>404</body></html>")
	}))
	defer server.Close()
	client, err := NewShareClient(shareCred(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	want := server.URL + " has no trace API (HTTP 404: endpoint not found)"
	state, found, err := client.GetTrace(context.Background(), "claude_code:abc")
	if err == nil || found || state != nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("GetTrace = %+v found=%v err=%v, want %q", state, found, err, want)
	}
	if _, err := client.PutTrace(context.Background(), "claude_code:abc", shareTrace("direct")); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("PutTrace err = %v, want %q", err, want)
	}
}

func TestShareClientRefusesAnotherSchema(t *testing.T) {
	client, err := NewShareClient(shareCred("https://beacon.example.com"), nil)
	if err != nil {
		t.Fatal(err)
	}
	trace := shareTrace("direct")
	trace.SchemaVersion = "beacon.share.v1"
	if _, err := client.PutTrace(context.Background(), "claude_code:abc", trace); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("err = %v", err)
	}
	if _, err := client.PutTrace(context.Background(), "claude_code:abc", asymptoteobserve.ShareTraceV1{}); err == nil {
		t.Fatal("expected a trace without a harness to be refused")
	}
}
