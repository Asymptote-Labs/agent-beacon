package asymptote

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

// fakeDashboard stands in for asymptotelabs.ai: it records the init request, and when the
// "browser" is opened it plays the approving user by redirecting to the CLI's callback with a
// one-time code, then validates the PKCE exchange.
type fakeDashboard struct {
	mu           sync.Mutex
	init         map[string]any
	exchange     map[string]any
	exchangeCode string
	ingestURL    string
	server       *httptest.Server
	initStatus   int
	exchangeFail string
}

func newFakeDashboard(t *testing.T) *fakeDashboard {
	t.Helper()
	fd := &fakeDashboard{exchangeCode: "code-123", initStatus: http.StatusCreated}
	fd.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		fd.mu.Lock()
		defer fd.mu.Unlock()
		switch r.URL.Path {
		case EnrollInitPath:
			fd.init = payload
			w.WriteHeader(fd.initStatus)
			if fd.initStatus >= 400 {
				_, _ = w.Write([]byte(`{"detail":"too many enrollment attempts"}`))
				return
			}
			_, _ = w.Write([]byte(`{"session_id":"s1","state":"` + payload["state"].(string) + `","expires_at":"2026-09-04T00:00:00Z"}`))
		case EnrollExchangePath:
			fd.exchange = payload
			if fd.exchangeFail != "" {
				w.WriteHeader(http.StatusGone)
				_, _ = w.Write([]byte(`{"detail":"` + fd.exchangeFail + `"}`))
				return
			}
			if payload["exchange_code"] != fd.exchangeCode {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"bad code"}`))
				return
			}
			ingest := fd.ingestURL
			if ingest == "" {
				ingest = "https://ingest.example.test"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_id":         "dev-1",
				"device_key":        "bcn_device_abcdefgh_" + strings.Repeat("k", 43),
				"key_prefix":        "bcn_device_abcdefgh",
				"ingest_url":        ingest,
				"organization_id":   "org-1",
				"organization_name": "Asymptote Test",
				"email":             "person@example.com",
				"scopes":            []string{"ingest:write"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fd.server.Close)
	return fd
}

// approve simulates the user clicking Approve: the dashboard redirects the browser to the
// CLI's callback with the exchange code and state taken from the approval URL.
func (fd *fakeDashboard) approve(t *testing.T) func(string) error {
	return func(approvalURL string) error {
		u, err := url.Parse(approvalURL)
		if err != nil {
			return err
		}
		if u.Path != EnrollPagePath {
			t.Errorf("browser opened %s, want %s", u.Path, EnrollPagePath)
		}
		if u.Query().Get("code_challenge") != "" {
			t.Errorf("approval URL must not carry the code challenge: %s", approvalURL)
		}
		state, port := u.Query().Get("state"), u.Query().Get("port")
		go func() {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/callback?state=%s&exchange_code=%s", port, state, fd.exchangeCode))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func device() DeviceInfo {
	return DeviceInfo{InstallID: "install-1", Hostname: "mac.local", OS: "darwin", Arch: "arm64", BeaconVersion: "test", InstallMode: "user"}
}

func TestEnrollRegistersDeviceOpensBrowserAndExchangesWithPKCE(t *testing.T) {
	fd := newFakeDashboard(t)
	var out strings.Builder
	result, err := Enroll(context.Background(), EnrollOptions{
		DashboardURL: fd.server.URL,
		Device:       device(),
		OpenBrowser:  fd.approve(t),
		HTTPClient:   fd.server.Client(),
		Out:          &out,
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if result.DeviceID != "dev-1" || !strings.HasPrefix(result.DeviceKey, "bcn_device_abcdefgh_") || result.IngestURL != "https://ingest.example.test" {
		t.Fatalf("unexpected result %+v", result)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.init["code_challenge_method"] != "S256" || fd.init["state"] == "" || fd.init["redirect_port"] == nil {
		t.Fatalf("init payload = %v", fd.init)
	}
	dev := fd.init["device"].(map[string]any)
	if dev["install_id"] != "install-1" || dev["hostname"] != "mac.local" || dev["install_mode"] != "user" {
		t.Fatalf("device payload = %v", dev)
	}
	if fd.exchange["state"] != fd.init["state"] || fd.exchange["code_verifier"] == "" || fd.exchange["exchange_code"] != "code-123" {
		t.Fatalf("exchange payload = %v", fd.exchange)
	}
	if !strings.Contains(out.String(), "Waiting for a member of your organization") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestEnrollSurfacesInitRejectionWithoutOpeningABrowser(t *testing.T) {
	fd := newFakeDashboard(t)
	fd.initStatus = http.StatusTooManyRequests
	opened := false
	_, err := Enroll(context.Background(), EnrollOptions{
		DashboardURL: fd.server.URL,
		Device:       device(),
		OpenBrowser:  func(string) error { opened = true; return nil },
		HTTPClient:   fd.server.Client(),
		Timeout:      2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "too many enrollment attempts") {
		t.Fatalf("expected the server detail, got %v", err)
	}
	if opened {
		t.Fatal("browser must not open when init fails")
	}
}

func TestEnrollReportsExchangeFailure(t *testing.T) {
	fd := newFakeDashboard(t)
	fd.exchangeFail = "exchange code has expired; restart enrollment"
	_, err := Enroll(context.Background(), EnrollOptions{
		DashboardURL: fd.server.URL,
		Device:       device(),
		OpenBrowser:  fd.approve(t),
		HTTPClient:   fd.server.Client(),
		Timeout:      5 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "exchange code has expired") {
		t.Fatalf("expected exchange failure, got %v", err)
	}
}

func TestEnrollRefusesInsecureIngestURLFromServer(t *testing.T) {
	fd := newFakeDashboard(t)
	fd.ingestURL = "http://ingest.example.test"
	_, err := Enroll(context.Background(), EnrollOptions{
		DashboardURL: fd.server.URL,
		Device:       device(),
		OpenBrowser:  fd.approve(t),
		HTTPClient:   fd.server.Client(),
		Timeout:      5 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("expected https enforcement, got %v", err)
	}
}

func TestEnrollRefusesInsecureDashboardAndMissingInstallID(t *testing.T) {
	if _, err := Enroll(context.Background(), EnrollOptions{DashboardURL: "http://dash.example.test", Device: device()}); err == nil {
		t.Fatal("expected error for http dashboard URL")
	}
	fd := newFakeDashboard(t)
	d := device()
	d.InstallID = ""
	if _, err := Enroll(context.Background(), EnrollOptions{DashboardURL: fd.server.URL, Device: d, HTTPClient: fd.server.Client()}); err == nil {
		t.Fatal("expected error for missing install id")
	}
}

// syncBuffer is an io.Writer safe to read from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestEnrollNoBrowserPrintsTheURL(t *testing.T) {
	fd := newFakeDashboard(t)
	var out syncBuffer
	approve := fd.approve(t)
	// NoBrowser prints the URL; the test approves from the printed text.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			text := out.String()
			if idx := strings.Index(text, "http://"); idx >= 0 {
				line := text[idx:]
				if nl := strings.IndexByte(line, '\n'); nl >= 0 {
					line = line[:nl]
				}
				_ = approve(line)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	_, err := Enroll(context.Background(), EnrollOptions{
		DashboardURL: fd.server.URL,
		Device:       device(),
		NoBrowser:    true,
		OpenBrowser:  func(string) error { t.Error("browser must not open with NoBrowser"); return nil },
		HTTPClient:   fd.server.Client(),
		Out:          &out,
		Timeout:      5 * time.Second,
	})
	<-done
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Open this URL in a browser") {
		t.Fatalf("output = %q", out.String())
	}
}
