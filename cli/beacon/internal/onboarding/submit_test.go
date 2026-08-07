package onboarding

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEndpointPrefersEnvOverride(t *testing.T) {
	t.Setenv(EndpointEnv, "http://127.0.0.1:9999/signup")
	if got := Endpoint(); got != "http://127.0.0.1:9999/signup" {
		t.Fatalf("Endpoint() = %q, want the override", got)
	}

	t.Setenv(EndpointEnv, "   ")
	if got := Endpoint(); got != DefaultEndpoint {
		t.Fatalf("Endpoint() = %q for a blank override, want the default", got)
	}
}

// Released binaries must not carry the raw <project-ref>.supabase.co host: that URL
// is permanent once shipped and leaks the project reference into every download.
func TestDefaultEndpointUsesOwnedDomain(t *testing.T) {
	if DefaultEndpoint != "https://auth.asymptotelabs.ai/functions/v1/beacon-signup" {
		t.Fatalf("DefaultEndpoint = %q, want the Asymptote-controlled domain", DefaultEndpoint)
	}
	if strings.Contains(DefaultEndpoint, "supabase.co") {
		t.Fatalf("DefaultEndpoint = %q must not embed the Supabase project host", DefaultEndpoint)
	}
	if !strings.HasPrefix(DefaultEndpoint, "https://") {
		t.Fatalf("DefaultEndpoint = %q must be https", DefaultEndpoint)
	}
}

func TestSubmitSendsExpectedRequest(t *testing.T) {
	var (
		gotMethod      string
		gotContentType string
		gotBody        Submission
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv(EndpointEnv, srv.URL)

	sub := NewSubmission("abc123", "shukan@asymptotelabs.ai", UsageWork, "v0.0.31", "user", []string{"claude_code", "cursor"})
	outcome, err := Submit(context.Background(), sub)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if outcome != OutcomeSubmitted {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeSubmitted)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Email != "shukan@asymptotelabs.ai" || gotBody.InstallID != "abc123" {
		t.Fatalf("body = %+v, want the submitted identity", gotBody)
	}
	if gotBody.EmailDomain != "asymptotelabs.ai" || gotBody.EmailDomainKind != DomainCorporate {
		t.Fatalf("body domain = %q/%q, want asymptotelabs.ai/corporate", gotBody.EmailDomain, gotBody.EmailDomainKind)
	}
	if gotBody.Source != SourceCLIOnboarding {
		t.Fatalf("source = %q, want %q", gotBody.Source, SourceCLIOnboarding)
	}
	if len(gotBody.DetectedRuntimes) != 2 {
		t.Fatalf("detected_runtimes = %v, want two entries", gotBody.DetectedRuntimes)
	}
	if gotBody.OS != runtime.GOOS || gotBody.Arch != runtime.GOARCH {
		t.Fatalf("os/arch = %q/%q, want %q/%q", gotBody.OS, gotBody.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if _, err := time.Parse(time.RFC3339, gotBody.ClientSentAt); err != nil {
		t.Fatalf("client_sent_at = %q is not RFC3339: %v", gotBody.ClientSentAt, err)
	}
}

// The payload is documented in the README, so an accidental new field is a
// disclosure bug rather than a harmless addition.
func TestSubmissionCarriesOnlyDocumentedFields(t *testing.T) {
	sub := NewSubmission("abc123", "dev@company.io", UsagePersonal, "v0.0.31", "user", nil)
	data, err := json.Marshal(sub)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	allowed := map[string]bool{
		"install_id": true, "email": true, "usage": true, "email_domain": true,
		"email_domain_kind": true, "os": true, "arch": true, "os_version": true,
		"beacon_version": true, "install_method": true, "install_mode": true,
		"detected_runtimes": true, "source": true, "client_sent_at": true,
	}
	for key := range raw {
		if !allowed[key] {
			t.Fatalf("undocumented field %q in the signup payload", key)
		}
	}
}

func TestNewSubmissionAlwaysEncodesRuntimesAsArray(t *testing.T) {
	sub := NewSubmission("abc123", "dev@company.io", UsageWork, "v0.0.31", "system", nil)
	if sub.DetectedRuntimes == nil {
		t.Fatalf("DetectedRuntimes = nil, want an empty slice so it encodes as []")
	}
	data, _ := json.Marshal(sub)
	if !strings.Contains(string(data), `"detected_runtimes":[]`) {
		t.Fatalf("payload = %s, want detected_runtimes as an empty array", data)
	}
}

func TestSubmitOutcomeByStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		want    string
		wantErr bool
	}{
		{"ok", http.StatusOK, OutcomeSubmitted, false},
		{"created", http.StatusCreated, OutcomeSubmitted, false},
		{"no content", http.StatusNoContent, OutcomeSubmitted, false},
		{"bad request is terminal", http.StatusBadRequest, OutcomeRejected, true},
		{"rate limited is terminal", http.StatusTooManyRequests, OutcomeRejected, true},
		{"payload too large is terminal", http.StatusRequestEntityTooLarge, OutcomeRejected, true},
		{"server error is retryable", http.StatusInternalServerError, OutcomePending, true},
		{"bad gateway is retryable", http.StatusBadGateway, OutcomePending, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			t.Setenv(EndpointEnv, srv.URL)

			outcome, err := Submit(context.Background(), Submission{})
			if outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", outcome, tc.want)
			}
			if tc.wantErr && err == nil {
				t.Fatalf("Submit returned nil error for status %d", tc.status)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Submit returned error for status %d: %v", tc.status, err)
			}
		})
	}
}

// A network failure must be retryable, never terminal: the lead is still good, the
// endpoint just was not reachable this minute.
func TestSubmitUnreachableEndpointIsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv(EndpointEnv, url)

	outcome, err := Submit(context.Background(), Submission{})
	if outcome != OutcomePending {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomePending)
	}
	if err == nil {
		t.Fatalf("Submit returned nil error for an unreachable endpoint")
	}
}

func TestSubmitRespectsContextCancellation(t *testing.T) {
	// The handler waits on an explicit release rather than only the request context:
	// a cancelled client does not always tear the connection down fast enough for
	// httptest.Server.Close to return, which would hang the suite instead of the
	// code under test.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv(EndpointEnv, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	outcome, err := Submit(ctx, Submission{})
	if elapsed := time.Since(start); elapsed > submitTimeout {
		t.Fatalf("Submit blocked for %s, want the context deadline to win", elapsed)
	}
	if outcome != OutcomePending || err == nil {
		t.Fatalf("outcome = %q err = %v, want pending with an error", outcome, err)
	}
}

func TestSubmitMalformedEndpointIsRejected(t *testing.T) {
	t.Setenv(EndpointEnv, "://not a url")
	outcome, err := Submit(context.Background(), Submission{})
	if outcome != OutcomeRejected || err == nil {
		t.Fatalf("outcome = %q err = %v, want rejected with an error", outcome, err)
	}
}

func TestInstallMethodClassifiesKnownPrefixes(t *testing.T) {
	// InstallMethod reads os.Executable, which in a test is the test binary, so this
	// asserts the classification is at least a known value rather than a guess.
	switch got := InstallMethod(); got {
	case MethodHomebrew, MethodPackage, MethodSource:
	default:
		t.Fatalf("InstallMethod() = %q, want one of homebrew/package/source", got)
	}
}

func TestLinuxOSVersionParsesOSRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	content := `NAME="Ubuntu"
VERSION="22.04.3 LTS (Jammy Jellyfish)"
ID=ubuntu
VERSION_ID="22.04"
PRETTY_NAME="Ubuntu 22.04.3 LTS"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := linuxOSVersion(path); got != "ubuntu 22.04" {
		t.Fatalf("linuxOSVersion() = %q, want %q", got, "ubuntu 22.04")
	}
}

func TestLinuxOSVersionFallsBackToPrettyName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	if err := os.WriteFile(path, []byte(`PRETTY_NAME="Some Distro"`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := linuxOSVersion(path); got != "Some Distro" {
		t.Fatalf("linuxOSVersion() = %q, want %q", got, "Some Distro")
	}
}

// OS version is decoration. A missing or unreadable file returns empty and the field
// is dropped from the payload rather than failing anything.
func TestLinuxOSVersionMissingFileIsEmpty(t *testing.T) {
	if got := linuxOSVersion(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Fatalf("linuxOSVersion() = %q for a missing file, want empty", got)
	}
}
