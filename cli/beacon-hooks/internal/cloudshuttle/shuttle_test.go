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

func TestUploadNoopsWithoutCredentials(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := Upload(context.Background(), Config{LogPath: logPath, Bucket: "bucket"}, true); err != nil {
		t.Fatalf("Upload returned error: %v", err)
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

func TestUploadRespectsInterval(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeState(statePath, state{LastUpload: time.Now().UTC().Format(time.RFC3339), LastSize: 10}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if uploadDue(Config{StatePath: statePath, Interval: time.Hour}, 11, time.Now().UTC()) {
		t.Fatal("uploadDue = true, want false inside interval")
	}
}

func TestStableFallbackRunIDReusesGeneratedValue(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "shuttle-state.json")
	first := stableFallbackRunID(statePath)
	second := stableFallbackRunID(statePath)
	if first == "" || second == "" || first != second {
		t.Fatalf("fallback run id not stable: first=%q second=%q", first, second)
	}
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
