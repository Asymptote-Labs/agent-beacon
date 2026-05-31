package updatecheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubSourceLatestParsesRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.0.12","html_url":"https://example.test/release"}`)
	}))
	defer server.Close()

	got, err := (GitHubSource{Client: server.Client(), Endpoint: server.URL}).Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	want := Release{Version: "v0.0.12", URL: "https://example.test/release"}
	if got != want {
		t.Fatalf("Latest = %#v, want %#v", got, want)
	}
}

func TestGitHubSourceLatestHandlesNonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	if _, err := (GitHubSource{Client: server.Client(), Endpoint: server.URL}).Latest(context.Background()); err == nil {
		t.Fatal("Latest error = nil, want non-200 error")
	}
}

func TestGitHubSourceLatestHandlesMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{bad-json`)
	}))
	defer server.Close()

	if _, err := (GitHubSource{Client: server.Client(), Endpoint: server.URL}).Latest(context.Background()); err == nil {
		t.Fatal("Latest error = nil, want JSON error")
	}
}
