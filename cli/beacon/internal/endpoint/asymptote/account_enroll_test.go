package asymptote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnrollAccountMintsDeviceCredentialWithoutBrowser(t *testing.T) {
	var authorization string
	var request accountEnrollRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AccountEnrollPath {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id":         "dev-1",
			"device_key":        "bcn_device_abcdefgh_" + strings.Repeat("k", 43),
			"key_prefix":        "bcn_device_abcdefgh",
			"ingest_url":        "https://ingest.beacon.sh",
			"organization_id":   "org-1",
			"organization_name": "Acme",
			"email":             "person@example.com",
			"scopes":            []string{"ingest:write"},
		})
	}))
	defer server.Close()

	result, err := EnrollAccount(context.Background(), AccountEnrollOptions{
		BaseURL:     server.URL,
		AccessToken: "bcn_cli_account_token",
		Device:      device(),
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("EnrollAccount returned error: %v", err)
	}
	if result.DeviceID != "dev-1" || authorization != "Bearer bcn_cli_account_token" {
		t.Fatalf("result=%#v authorization=%q", result, authorization)
	}
	if request.Device.InstallID != "install-1" || request.Device.Hostname != "mac.local" {
		t.Fatalf("device request = %#v", request.Device)
	}
}

func TestEnrollAccountRejectsUnsafeInputsAndServerErrors(t *testing.T) {
	if _, err := EnrollAccount(context.Background(), AccountEnrollOptions{
		BaseURL: "http://example.com", AccessToken: "bcn_cli_token", Device: device(),
	}); err == nil {
		t.Fatal("expected insecure URL rejection")
	}
	if _, err := EnrollAccount(context.Background(), AccountEnrollOptions{
		BaseURL: "https://beacon.sh", AccessToken: "not-a-cli-token", Device: device(),
	}); err == nil {
		t.Fatal("expected account token rejection")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"device:enroll scope required"}`))
	}))
	defer server.Close()
	_, err := EnrollAccount(context.Background(), AccountEnrollOptions{
		BaseURL: server.URL, AccessToken: "bcn_cli_token", Device: device(), HTTPClient: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "device:enroll scope required") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnrollAccountDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	_, err := EnrollAccount(context.Background(), AccountEnrollOptions{
		BaseURL: redirect.URL, AccessToken: "bcn_cli_token", Device: device(), HTTPClient: redirect.Client(),
	})
	if err == nil || leaked != "" {
		t.Fatalf("error=%v leaked authorization=%q", err, leaked)
	}
}
