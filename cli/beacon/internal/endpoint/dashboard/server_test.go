package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusUsesExplicitRuntimeLogPath(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	handler, err := Handler(Options{UserMode: true, LogPath: logPath})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var status StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if status.LogPath != logPath {
		t.Fatalf("LogPath = %q, want %q", status.LogPath, logPath)
	}
	if status.RuntimeLog.EffectiveLogPath != logPath {
		t.Fatalf("RuntimeLog.EffectiveLogPath = %q, want %q", status.RuntimeLog.EffectiveLogPath, logPath)
	}
}

func TestStaticDashboardPagesServe(t *testing.T) {
	handler, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}

	cases := []struct {
		path string
		want string
	}{
		{path: "/", want: "Beacon Endpoint Log Search"},
		{path: "/overview.html", want: "Beacon Endpoint Security Overview"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body did not contain %q", tc.want)
			}
		})
	}
}
