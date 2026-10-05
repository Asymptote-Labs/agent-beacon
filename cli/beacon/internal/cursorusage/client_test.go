package cursorusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "filtered_usage_events.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testClient(url string) *Client {
	c := NewClient("key_test_secret")
	c.BaseURL = url
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestDecodePageReadsEveryField(t *testing.T) {
	page, err := DecodePage(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 3 || page.HasNextPage == nil || *page.HasNextPage {
		t.Fatalf("pagination = %d %v", page.TotalCount, page.HasNextPage)
	}
	if len(page.Events) != 3 {
		t.Fatalf("events = %d", len(page.Events))
	}

	first := page.Events[0]
	if !first.Timestamp.Equal(time.UnixMilli(1750979225854)) {
		t.Errorf("timestamp = %v", first.Timestamp)
	}
	if first.Model != "claude-4-opus" || first.Kind != "Usage-based" || !first.MaxMode || !first.TokenBased {
		t.Errorf("identity = %+v", first)
	}
	want := TokenUsage{InputTokens: 126, OutputTokens: 450, CacheWriteTokens: 6112, CacheReadTokens: 11964}
	got := first.TokenUsage
	got.TotalCents = nil
	if got != want {
		t.Errorf("tokens = %+v, want %+v", got, want)
	}
	if first.TokenUsage.TotalCents == nil || *first.TokenUsage.TotalCents != 20.18232 {
		t.Errorf("totalCents = %v", first.TokenUsage.TotalCents)
	}
	if first.ChargedCents == nil || *first.ChargedCents != 20.18232 {
		t.Errorf("chargedCents = %v", first.ChargedCents)
	}
	if first.CursorTokenFeeCents == nil || *first.CursorTokenFeeCents != 0.5 {
		t.Errorf("cursorTokenFee = %v", first.CursorTokenFeeCents)
	}
	if first.RequestsCosts == nil || *first.RequestsCosts != 5 {
		t.Errorf("requestsCosts = %v", first.RequestsCosts)
	}
	if first.Chargeable == nil || !*first.Chargeable || first.Headless {
		t.Errorf("flags = %v %v", first.Chargeable, first.Headless)
	}
	// userId arrives as a number on one event and a string on the next; both read the same.
	if first.UserID != "152683922" || page.Events[1].UserID != "152683922" {
		t.Errorf("user ids = %q %q", first.UserID, page.Events[1].UserID)
	}

	second := page.Events[1]
	if second.TokenUsage.InputTokens != 42 || !second.Headless {
		t.Errorf("second = %+v", second)
	}
	if second.ChargedCents == nil || *second.ChargedCents != 0 {
		t.Errorf("an explicit zero charge must survive as zero, got %v", second.ChargedCents)
	}

	third := page.Events[2]
	if !third.Timestamp.Equal(time.Date(2025, 6, 26, 23, 5, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339 timestamp = %v", third.Timestamp)
	}
	if third.Kind != "Included in Pro" {
		t.Errorf("kindLabel fallback = %q", third.Kind)
	}
	if third.ChargedCents != nil || third.TokenUsage != (TokenUsage{}) {
		t.Errorf("absent amounts must stay absent: %+v", third)
	}
}

func TestDecodePageAcceptsLegacyEnvelopeKey(t *testing.T) {
	page, err := DecodePage([]byte(`{"totalUsageEventsCount":"1","usageEvents":[{"timestamp":1748700000000,"model":"m","tokenUsage":{"inputTokens":1.0,"outputTokens":null}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 1 || len(page.Events) != 1 || page.Events[0].TokenUsage.InputTokens != 1 {
		t.Fatalf("page = %+v", page)
	}
	if page.HasNextPage != nil {
		t.Fatalf("no pagination block must leave HasNextPage unset")
	}
}

func TestDecodePageRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"negative charge":   `{"usageEventsDisplay":[{"timestamp":"1","model":"m","chargedCents":-1}]}`,
		"negative tokens":   `{"usageEventsDisplay":[{"timestamp":"1","model":"m","tokenUsage":{"inputTokens":-5}}]}`,
		"fractional tokens": `{"usageEventsDisplay":[{"timestamp":"1","model":"m","tokenUsage":{"inputTokens":1.5}}]}`,
		"missing timestamp": `{"usageEventsDisplay":[{"model":"m"}]}`,
		"zero timestamp":    `{"usageEventsDisplay":[{"timestamp":"0","model":"m"}]}`,
		"bad timestamp":     `{"usageEventsDisplay":[{"timestamp":"yesterday","model":"m"}]}`,
		"string amount":     `{"usageEventsDisplay":[{"timestamp":"1","model":"m","chargedCents":"lots"}]}`,
		"not json":          `<html>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePage([]byte(body)); err == nil {
				t.Fatalf("expected an error for %s", body)
			}
		})
	}
}

func TestListUsageEventsSendsTheAdminRequest(t *testing.T) {
	type requestBody struct {
		StartDate int64  `json:"startDate"`
		EndDate   int64  `json:"endDate"`
		Page      int    `json:"page"`
		PageSize  int    `json:"pageSize"`
		Email     string `json:"email"`
		UserID    int64  `json:"userId"`
	}
	fixture := readFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != UsageEventsPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "key_test_secret" || pass != "" {
			t.Errorf("basic auth = %q %q %v", user, pass, ok)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "beacon-test" {
			t.Errorf("user agent = %q", got)
		}
		var body requestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		want := requestBody{StartDate: 1777593600000, EndDate: 1777766399000, Page: 2, PageSize: 50, Email: "member@example.com", UserID: 152683922}
		if body != want {
			t.Errorf("body = %+v, want %+v", body, want)
		}
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	c.UserAgent = "beacon-test"
	page, err := c.ListUsageEvents(t.Context(), Query{
		StartDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 5, 2, 23, 59, 59, 0, time.UTC),
		Email:     " member@example.com ",
		UserID:    "152683922",
		PageSize:  50,
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("events = %d", len(page.Events))
	}
}

func TestListUsageEventsOmitsUnsetFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["email"]; ok {
			t.Errorf("email sent when unset: %v", body)
		}
		if _, ok := body["userId"]; ok {
			t.Errorf("userId sent when unset: %v", body)
		}
		if body["pageSize"] != float64(100) {
			t.Errorf("default page size = %v", body["pageSize"])
		}
		_, _ = w.Write([]byte(`{"usageEventsDisplay":[]}`))
	}))
	t.Cleanup(srv.Close)
	if _, err := testClient(srv.URL).ListUsageEvents(t.Context(), Query{}, 1); err != nil {
		t.Fatal(err)
	}
}

func TestListUsageEventsCapsPageSize(t *testing.T) {
	body, err := requestBody(Query{PageSize: 50000}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"pageSize":1000`) {
		t.Fatalf("body = %s", body)
	}
}

func TestListUsageEventsRejectsNonNumericUserID(t *testing.T) {
	_, err := testClient("https://example.test").ListUsageEvents(t.Context(), Query{UserID: "user_123"}, 1)
	if err == nil || !strings.Contains(err.Error(), `invalid Cursor user ID "user_123"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestListUsageEventsRequiresAKey(t *testing.T) {
	c := testClient("https://example.test")
	c.APIKey = "  "
	if _, err := c.ListUsageEvents(t.Context(), Query{}, 1); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchAllFollowsHasNextPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Page int `json:"page"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		next := body.Page < 3
		// Full pages with hasNextPage true, then a final page with hasNextPage false. The total
		// count is deliberately wrong so the test proves hasNextPage, not the count, decides.
		resp := map[string]any{
			"totalUsageEventsCount": 1,
			"pagination":            map[string]any{"hasNextPage": next},
			"usageEventsDisplay": []map[string]any{
				{"timestamp": time.UnixMilli(int64(1000 + body.Page)).UnixMilli(), "model": "m"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	events, err := testClient(srv.URL).FetchAll(t.Context(), Query{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || calls.Load() != 3 {
		t.Fatalf("events = %d, calls = %d", len(events), calls.Load())
	}
}

func TestFetchAllWithoutPaginationUsesCountAndShortPage(t *testing.T) {
	fixture := readFixture(t)
	var env map[string]json.RawMessage
	_ = json.Unmarshal(fixture, &env)
	var all []json.RawMessage
	_ = json.Unmarshal(env["usageEventsDisplay"], &all)

	t.Run("total count", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"totalUsageEventsCount": 3,
				"usageEventsDisplay":    all[body.Page-1 : body.Page],
			})
		}))
		t.Cleanup(srv.Close)
		events, err := testClient(srv.URL).FetchAll(t.Context(), Query{PageSize: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 3 || calls.Load() != 3 {
			t.Fatalf("events = %d calls = %d", len(events), calls.Load())
		}
	})

	t.Run("host ignores page numbers", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"usageEventsDisplay": all})
		}))
		t.Cleanup(srv.Close)
		events, err := testClient(srv.URL).FetchAll(t.Context(), Query{PageSize: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 3 || calls.Load() != 1 {
			t.Fatalf("a short page must end paging: events = %d calls = %d", len(events), calls.Load())
		}
	})
}

func TestRetriesRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"usageEventsDisplay":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	var waits []time.Duration
	c := testClient(srv.URL)
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	if _, err := c.ListUsageEvents(t.Context(), Query{}, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if len(waits) != 2 || waits[0] != 7*time.Second {
		t.Fatalf("waits = %v", waits)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).ListUsageEvents(t.Context(), Query{}, 1)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != maxAttempts {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestAuthFailureIsNotRetriedAndNeverEchoesTheKeyOrBody(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key key_test_secret"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).ListUsageEvents(t.Context(), Query{}, 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Fatalf("a 401 must not be retried, calls = %d", calls.Load())
	}
	if strings.Contains(err.Error(), "key_test_secret") {
		t.Fatalf("error leaks the key: %v", err)
	}
	if !strings.Contains(err.Error(), "Admin API key") {
		t.Fatalf("error should say what to check: %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	cases := map[string]time.Duration{
		"":      2 * time.Second,
		"3":     3 * time.Second,
		"9999":  maxRetryWait,
		"later": 2 * time.Second,
	}
	for header, want := range cases {
		if got := retryDelay(header); got != want {
			t.Errorf("retryDelay(%q) = %v, want %v", header, got, want)
		}
	}
	if got := retryDelay(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("past HTTP date = %v", got)
	}
}

func TestOversizedResponseIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usageEventsDisplay":[],"pad":"`))
		chunk := []byte(strings.Repeat("x", 1<<20))
		for i := 0; i < (maxResponseBytes>>20)+1; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).ListUsageEvents(t.Context(), Query{}, 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	c := testClient(srv.URL)
	c.Sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if _, err := c.ListUsageEvents(ctx, Query{}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	ok := map[string]string{
		"":                          DefaultBaseURL,
		"https://api.cursor.com/":   "https://api.cursor.com",
		"http://127.0.0.1:8080":     "http://127.0.0.1:8080",
		"http://localhost:9":        "http://localhost:9",
		"http://[::1]:9":            "http://[::1]:9",
		"https://proxy.example/api": "https://proxy.example/api",
	}
	for in, want := range ok {
		got, err := ValidateBaseURL(in)
		if err != nil || got != want {
			t.Errorf("ValidateBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"http://api.cursor.com",
		"ftp://api.cursor.com",
		"https://user:pass@api.cursor.com",
		"https://api.cursor.com?x=1",
		"api.cursor.com",
		"https://",
	} {
		if _, err := ValidateBaseURL(bad); err == nil {
			t.Errorf("ValidateBaseURL(%q) accepted", bad)
		}
	}
}

func TestPlainHTTPToARemoteHostNeverSendsTheKey(t *testing.T) {
	var reached atomic.Bool
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached.Store(true)
		return nil, errors.New("must not be called")
	})
	c := testClient("http://api.cursor.com")
	c.HTTPClient = &http.Client{Transport: transport}
	if _, err := c.ListUsageEvents(t.Context(), Query{}, 1); err == nil {
		t.Fatal("expected refusal")
	}
	if reached.Load() {
		t.Fatal("request was sent over plain http")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
