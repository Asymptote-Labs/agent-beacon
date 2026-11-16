package asymptote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	requests []*http.Request
	bodies   []BackfillReport
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
	w.WriteHeader(status)
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
