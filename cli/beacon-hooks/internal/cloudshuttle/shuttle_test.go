package cloudshuttle

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestObjectNamePartitionsByProviderUserRepoAndRun(t *testing.T) {
	got := ObjectName(Config{
		Prefix:     "agent-traces/customer=test",
		Provider:   "claude_code_web",
		UserID:     "user-1",
		Repository: "asymptote-labs/agent-beacon",
		RunID:      "cse_123",
	})
	want := "agent-traces/customer=test/provider=claude_code_web/user_id=user-1/repo=asymptote-labs/agent-beacon/run_id=cse_123/runtime.jsonl"
	if got != want {
		t.Fatalf("ObjectName = %q, want %q", got, want)
	}
}

func TestObjectNameUsesCursorCloudProvider(t *testing.T) {
	got := ObjectName(Config{
		Prefix:   "agent-traces",
		Provider: "cursor_cloud",
		UserID:   "user-1",
		RunID:    "manual-123",
	})
	want := "agent-traces/provider=cursor_cloud/user_id=user-1/run_id=manual-123/runtime.jsonl"
	if got != want {
		t.Fatalf("ObjectName = %q, want %q", got, want)
	}
}

func TestObjectNameUsesCodexCloudProvider(t *testing.T) {
	got := ObjectName(Config{
		Prefix:   "agent-traces",
		Provider: "codex_cloud",
		UserID:   "user-1",
		RunID:    "codex-session",
	})
	want := "agent-traces/provider=codex_cloud/user_id=user-1/run_id=codex-session/runtime.jsonl"
	if got != want {
		t.Fatalf("ObjectName = %q, want %q", got, want)
	}
}

func TestUploadNoopsWithoutCredentials(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := Upload(context.Background(), Config{LogPath: logPath, Bucket: "bucket"}, true); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
}

func TestResetFromEnvRemovesCloudRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "state.json")
	for _, path := range []string{logPath, logPath + ".lock", statePath} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", statePath)

	if err := ResetFromEnv(); err != nil {
		t.Fatalf("ResetFromEnv returned error: %v", err)
	}
	for _, path := range []string{logPath, logPath + ".lock", statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, stat err=%v", path, err)
		}
	}
}

func TestUploadSendsJSONLToGCS(t *testing.T) {
	key := mustRSAKey(t)
	var uploadedPath, uploadedAuth, uploadedType, uploadedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
				t.Fatalf("unexpected token request: %s", r.Form.Encode())
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "test-token", "token_type": "Bearer", "expires_in": 3600})
		default:
			uploadedPath = r.URL.EscapedPath()
			uploadedAuth = r.Header.Get("Authorization")
			uploadedType = r.Header.Get("Content-Type")
			data, _ := io.ReadAll(r.Body)
			uploadedBody = string(data)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	creds := serviceAccount{
		ClientEmail: "beacon@example.iam.gserviceaccount.com",
		PrivateKey:  pemKey(t, key),
		TokenURI:    server.URL + "/token",
	}
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	cfg := Config{
		LogPath:        logPath,
		StatePath:      filepath.Join(t.TempDir(), "state.json"),
		Bucket:         "bucket",
		Prefix:         "prefix",
		CredentialsB64: base64.StdEncoding.EncodeToString(credsJSON),
		Provider:       "claude_code_web",
		UserID:         "user",
		RunID:          "run",
		GCSEndpoint:    server.URL,
	}
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if uploadedAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want bearer token", uploadedAuth)
	}
	if uploadedType != contentTypeJSONL {
		t.Fatalf("Content-Type = %q, want %q", uploadedType, contentTypeJSONL)
	}
	if !strings.Contains(uploadedPath, "/bucket/prefix/provider=claude_code_web/user_id=user/run_id=run/runtime.jsonl") {
		t.Fatalf("upload path = %q", uploadedPath)
	}
	if uploadedBody != "{\"event\":\"ok\"}\n" {
		t.Fatalf("uploaded body = %q", uploadedBody)
	}
}

func TestResolveRunIDUsesOnlyEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "cse_env")
	if got := resolveRunID(); got != "cse_env" {
		t.Fatalf("resolveRunID = %q, want cse_env", got)
	}
}

func TestResolveRunIDFromRuntimeLogUsesCodexSession(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte(`{"session":{"id":"codex-session"}}
`), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if got := resolveRunIDFromLog(logPath); got != "codex-session" {
		t.Fatalf("resolveRunIDFromLog = %q, want codex-session", got)
	}
}

func TestUploadSkipsUnchangedLogWhenNotForced(t *testing.T) {
	key := mustRSAKey(t)
	var uploads atomic.Int64
	server := fakeGCSServer(t, key, &uploads, nil)
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfg := testUploadConfig(t, key, server.URL, logPath)
	if err := Upload(context.Background(), cfg, false); err != nil {
		t.Fatalf("first Upload returned error: %v", err)
	}
	if err := Upload(context.Background(), cfg, false); err != nil {
		t.Fatalf("second Upload returned error: %v", err)
	}
	if got := uploads.Load(); got != 1 {
		t.Fatalf("uploads after unchanged log = %d, want 1", got)
	}
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n{\"event\":\"next\"}\n"), 0644); err != nil {
		t.Fatalf("append log: %v", err)
	}
	if err := Upload(context.Background(), cfg, false); err != nil {
		t.Fatalf("third Upload returned error: %v", err)
	}
	if got := uploads.Load(); got != 2 {
		t.Fatalf("uploads after changed log = %d, want 2", got)
	}
}

func TestWatchUploadsInitialLog(t *testing.T) {
	key := mustRSAKey(t)
	var uploads atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := fakeGCSServer(t, key, &uploads, nil)
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfg := testUploadConfig(t, key, server.URL, logPath)
	t.Setenv("BEACON_ENDPOINT_LOG", cfg.LogPath)
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", cfg.StatePath)
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", cfg.Bucket)
	t.Setenv("BEACON_CLOUD_GCS_PREFIX", cfg.Prefix)
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", cfg.CredentialsB64)
	t.Setenv("BEACON_RUN_PROVIDER", cfg.Provider)
	t.Setenv("BEACON_RUN_ID", cfg.RunID)
	t.Setenv("BEACON_CLOUD_GCS_ENDPOINT", cfg.GCSEndpoint)

	time.AfterFunc(20*time.Millisecond, cancel)
	if err := Watch(ctx, time.Hour); err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	if got := uploads.Load(); got != 1 {
		t.Fatalf("uploads = %d, want 1", got)
	}
}

func TestWatchContinuesAfterUploadError(t *testing.T) {
	key := mustRSAKey(t)
	var uploads atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := fakeFlakyGCSServer(t, key, &uploads, cancel)
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"event\":\"ok\"}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfg := testUploadConfig(t, key, server.URL, logPath)
	t.Setenv("BEACON_ENDPOINT_LOG", cfg.LogPath)
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", cfg.StatePath)
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", cfg.Bucket)
	t.Setenv("BEACON_CLOUD_GCS_PREFIX", cfg.Prefix)
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", cfg.CredentialsB64)
	t.Setenv("BEACON_RUN_PROVIDER", cfg.Provider)
	t.Setenv("BEACON_RUN_ID", cfg.RunID)
	t.Setenv("BEACON_CLOUD_GCS_ENDPOINT", cfg.GCSEndpoint)

	if err := Watch(ctx, 5*time.Millisecond); err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	if got := uploads.Load(); got < 2 {
		t.Fatalf("uploads = %d, want retry after initial failure", got)
	}
}

func TestUploadNoopsWithoutRunID(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfg := Config{
		LogPath:        logPath,
		Bucket:         "bucket",
		CredentialsB64: "invalid-but-should-not-be-read",
	}
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
}

func testUploadConfig(t *testing.T, key *rsa.PrivateKey, endpoint, logPath string) Config {
	t.Helper()
	creds := serviceAccount{
		ClientEmail: "beacon@example.iam.gserviceaccount.com",
		PrivateKey:  pemKey(t, key),
		TokenURI:    endpoint + "/token",
	}
	credsJSON, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return Config{
		LogPath:        logPath,
		StatePath:      filepath.Join(t.TempDir(), "state.json"),
		Bucket:         "bucket",
		Prefix:         "prefix",
		CredentialsB64: base64.StdEncoding.EncodeToString(credsJSON),
		Provider:       "codex_cloud",
		UserID:         "user",
		RunID:          "run",
		GCSEndpoint:    endpoint,
	}
}

func fakeGCSServer(t *testing.T, key *rsa.PrivateKey, uploads *atomic.Int64, afterUpload func()) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "test-token", "token_type": "Bearer", "expires_in": 3600})
		default:
			defer func() {
				if afterUpload != nil {
					go afterUpload()
				}
			}()
			uploads.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	_ = key
	return server
}

func fakeFlakyGCSServer(t *testing.T, key *rsa.PrivateKey, uploads *atomic.Int64, cancel func()) *httptest.Server {
	t.Helper()
	var failed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "test-token", "token_type": "Bearer", "expires_in": 3600})
		default:
			count := uploads.Add(1)
			if !failed.Swap(true) {
				http.Error(w, "temporary error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			if count >= 2 {
				time.AfterFunc(5*time.Millisecond, cancel)
			}
		}
	}))
	_ = key
	return server
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func pemKey(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	data, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: data}))
}
