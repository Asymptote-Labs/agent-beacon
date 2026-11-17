package asymptote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
)

func sessionLine(ts, harness, session, id string) string {
	return `{"timestamp":"` + ts + `","event":{"id":"` + id + `"},"harness":{"name":"` + harness + `","collection_method":"poll"},"session":{"id":"` + session + `"}}`
}

func TestStageBackfillCountsTheSessionsItStages(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	trimmed := sessionLine("2026-09-20T00:00:00Z", "codex", "gone", "e0")
	writeLines(t, logPath,
		trimmed,
		sessionLine("2026-09-21T00:00:00Z", "codex", "s1", "e1"),
		sessionLine("2026-09-21T00:00:01Z", "codex", "s1", "e2"),
		sessionLine("2026-09-21T00:00:02Z", "claude_code", "s1", "e3"),
		backfillTestLine("2026-09-21T00:00:03Z", "poll", "no-session"),
	)
	full, err := StageBackfill(logPath, BackfillOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if full.Events != 5 || full.Sessions != 3 {
		t.Fatalf("full = %+v, want 5 events in 3 sessions", full)
	}
	cut, err := StageBackfill(logPath, BackfillOptions{MaxBytes: full.Bytes - int64(len(trimmed)+1)})
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Trimmed || cut.Events != 4 || cut.Sessions != 2 {
		t.Fatalf("trimmed = %+v, want 4 events in 2 sessions", cut)
	}
}

type reportServer struct {
	mu       sync.Mutex
	statuses []int
	// retryAfter is the Retry-After header of each response in turn, "" for none.
	retryAfter []string
	requests   []*http.Request
	bodies     []BackfillReport
}

func (s *reportServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body BackfillReport
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	s.requests = append(s.requests, r)
	s.bodies = append(s.bodies, body)
	status := http.StatusOK
	if len(s.statuses) > 0 {
		status, s.statuses = s.statuses[0], s.statuses[1:]
	}
	if len(s.retryAfter) > 0 {
		if s.retryAfter[0] != "" {
			w.Header().Set("Retry-After", s.retryAfter[0])
		}
		s.retryAfter = s.retryAfter[1:]
	}
	w.WriteHeader(status)
}

// recordBackfillWaits makes the report's pauses instant and returns what it asked to wait.
var oldBackfillReportSleep = backfillReportSleep

func recordBackfillWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := backfillReportSleep
	backfillReportSleep = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	t.Cleanup(func() { backfillReportSleep = old })
	return &waits
}

func withDeviceKey(t *testing.T) string {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	key := "bcn_device_abcdefgh_" + strings.Repeat("k", 43)
	if err := WriteSecrets(true, key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestReportBackfillPostsCountsWithTheDeviceKey(t *testing.T) {
	key := withDeviceKey(t)
	recordBackfillWaits(t)
	rs := &reportServer{statuses: []int{http.StatusServiceUnavailable}}
	server := httptest.NewServer(http.HandlerFunc(rs.handler))
	defer server.Close()

	ReportBackfill(context.Background(), true, server.URL, BackfillReport{SessionCount: 3, EventCount: 41})
	if len(rs.requests) != 2 {
		t.Fatalf("requests = %d, want the 503 and one retry", len(rs.requests))
	}
	req := rs.requests[1]
	if req.Method != http.MethodPost || req.URL.Path != BackfillReportPath || req.Header.Get("Authorization") != "Bearer "+key || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request = %s %s %v", req.Method, req.URL.Path, req.Header)
	}
	if rs.bodies[1] != (BackfillReport{SessionCount: 3, EventCount: 41}) {
		t.Fatalf("body = %+v", rs.bodies[1])
	}
}

func TestReportBackfillGivesUpQuietly(t *testing.T) {
	withDeviceKey(t)
	// An older server has no such endpoint: one request, no retry.
	rs := &reportServer{statuses: []int{http.StatusNotFound, http.StatusNotFound}}
	server := httptest.NewServer(http.HandlerFunc(rs.handler))
	defer server.Close()
	ReportBackfill(context.Background(), true, server.URL, BackfillReport{})
	if len(rs.requests) != 1 {
		t.Fatalf("requests = %d after a 404, want 1", len(rs.requests))
	}

	// A server that never answers costs at most the budget.
	oldBudget := backfillReportBudget
	backfillReportBudget = 300 * time.Millisecond
	defer func() { backfillReportBudget = oldBudget }()
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)
	start := time.Now()
	ReportBackfill(context.Background(), true, hang.URL, BackfillReport{})
	if elapsed := time.Since(start); elapsed > backfillReportBudget+time.Second {
		t.Fatalf("a hung server held the report for %v", elapsed)
	}

	// Plain http to a remote host is never sent the key.
	ReportBackfill(context.Background(), true, "http://ingest.example.test", BackfillReport{})
}

func TestReportBackfillRetriesThrottlingAndServerErrors(t *testing.T) {
	withDeviceKey(t)
	for _, tc := range []struct {
		name       string
		statuses   []int
		retryAfter []string
		requests   int
		waits      []time.Duration
	}{
		// The per-device ingest limit answers 429 while the forwarder uploads the backfill.
		{name: "429 then 200", statuses: []int{429, 200}, requests: 2, waits: []time.Duration{backfillReportBackoff}},
		{name: "429 with Retry-After", statuses: []int{429, 200}, retryAfter: []string{"2"}, requests: 2, waits: []time.Duration{2 * time.Second}},
		{name: "Retry-After is capped", statuses: []int{429, 200}, retryAfter: []string{"3600"}, requests: 2, waits: []time.Duration{backfillReportMaxRetryAfter}},
		{name: "unreadable Retry-After backs off", statuses: []int{429, 200}, retryAfter: []string{"soon"}, requests: 2, waits: []time.Duration{backfillReportBackoff}},
		{name: "503 then 200", statuses: []int{503, 200}, retryAfter: []string{"1"}, requests: 2, waits: []time.Duration{time.Second}},
		{name: "backoff doubles", statuses: []int{429, 503, 500, 200}, requests: 4, waits: []time.Duration{backfillReportBackoff, 2 * backfillReportBackoff, 4 * backfillReportBackoff}},
		{name: "400 is final", statuses: []int{400, 200}, requests: 1},
		{name: "401 is final", statuses: []int{401, 200}, requests: 1},
		{name: "409 is final", statuses: []int{409, 200}, requests: 1},
		{
			name:     "attempts are bounded",
			statuses: []int{429, 429, 429, 429, 429, 429, 429},
			requests: backfillReportAttempts,
			waits:    []time.Duration{backfillReportBackoff, 2 * backfillReportBackoff, 4 * backfillReportBackoff, 8 * backfillReportBackoff},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waits := recordBackfillWaits(t)
			rs := &reportServer{statuses: tc.statuses, retryAfter: tc.retryAfter}
			server := httptest.NewServer(http.HandlerFunc(rs.handler))
			defer server.Close()
			ReportBackfill(context.Background(), true, server.URL, BackfillReport{SessionCount: 1, EventCount: 2})
			if len(rs.requests) != tc.requests {
				t.Fatalf("requests = %d, want %d", len(rs.requests), tc.requests)
			}
			for i, body := range rs.bodies {
				if body != (BackfillReport{SessionCount: 1, EventCount: 2}) {
					t.Fatalf("body %d = %+v", i, body)
				}
			}
			if !slices.Equal(*waits, tc.waits) {
				t.Fatalf("waits = %v, want %v", *waits, tc.waits)
			}
		})
	}
}

func TestReportBackfillStopsWhenTheBudgetIsSpent(t *testing.T) {
	withDeviceKey(t)
	oldBudget := backfillReportBudget
	backfillReportBudget = 2 * time.Second
	t.Cleanup(func() { backfillReportBudget = oldBudget })

	// A Retry-After past what is left of the budget is not waited out.
	waits := recordBackfillWaits(t)
	rs := &reportServer{statuses: []int{429, 200}, retryAfter: []string{"4"}}
	server := httptest.NewServer(http.HandlerFunc(rs.handler))
	defer server.Close()
	start := time.Now()
	ReportBackfill(context.Background(), true, server.URL, BackfillReport{})
	if len(rs.requests) != 1 || len(*waits) != 0 {
		t.Fatalf("requests = %d, waits = %v; want one request and no wait", len(rs.requests), *waits)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("giving up took %v", elapsed)
	}

	// With real pauses, a server that keeps throttling costs at most the budget.
	backfillReportSleep = oldBackfillReportSleep
	backfillReportBudget = 1200 * time.Millisecond
	throttled := &reportServer{statuses: []int{429, 429, 429, 429, 429}}
	busy := httptest.NewServer(http.HandlerFunc(throttled.handler))
	defer busy.Close()
	start = time.Now()
	ReportBackfill(context.Background(), true, busy.URL, BackfillReport{})
	if elapsed := time.Since(start); elapsed > backfillReportBudget+500*time.Millisecond {
		t.Fatalf("a throttling server held the report for %v", elapsed)
	}
	// 0s: 429, wait 500ms; 0.5s: 429, a 1s wait no longer fits.
	if len(throttled.requests) != 2 {
		t.Fatalf("requests = %d, want 2 within the budget", len(throttled.requests))
	}
}

func TestBackfillReportRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"", 0, false},
		{"0", 0, true},
		{" 3 ", 3 * time.Second, true},
		{"-1", 0, false},
		{"86400", backfillReportMaxRetryAfter, true},
		{now.Add(2 * time.Second).Format(http.TimeFormat), 2 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{now.Add(time.Hour).Format(http.TimeFormat), backfillReportMaxRetryAfter, true},
		{"later", 0, false},
	} {
		got, ok := backfillReportRetryAfter(tc.header, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("backfillReportRetryAfter(%q) = %v, %v; want %v, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestConnectSendsPrivacyModeAndReportsItsBackfillOnce(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	isolateVectorDiscovery(t)
	var reports []BackfillReport
	var reportedURL string
	old := sendBackfillReport
	sendBackfillReport = func(_ context.Context, userMode bool, ingestURL string, report BackfillReport) {
		reportedURL = ingestURL
		reports = append(reports, report)
	}
	t.Cleanup(func() { sendBackfillReport = old })

	fd := newFakeDashboard(t)
	opts := connectOptions(t, fd, &fakeForwarder{supported: true}, fakeVector(t, "0.56.0", 0))
	opts.PrivacyMode = "metadata-only"
	writeLines(t, opts.LogPath,
		sessionLine(time.Now().UTC().Format(time.RFC3339), "codex", "s1", "e1"),
		sessionLine(time.Now().UTC().Format(time.RFC3339), "codex", "s2", "e2"))
	opts.Backfill = &ConnectBackfill{}
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := fd.init["device"].(map[string]any)["privacy_mode"]; got != "metadata_only" {
		t.Fatalf("enroll device privacy_mode = %v", got)
	}
	if len(reports) != 1 || reports[0] != (BackfillReport{SessionCount: 2, EventCount: 2}) || reportedURL != "https://ingest.example.test" {
		t.Fatalf("reports = %+v to %s", reports, reportedURL)
	}

	// A connect with nothing to backfill still reports, with zeros.
	reports = nil
	opts.Backfill = nil
	if _, err := Connect(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0] != (BackfillReport{}) {
		t.Fatalf("reports without a backfill = %+v", reports)
	}
}
