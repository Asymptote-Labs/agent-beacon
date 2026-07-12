package cloudshuttle

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestConfigFromEnvSelectsS3(t *testing.T) {
	t.Setenv("BEACON_CLOUD_UPLOAD", "s3")
	t.Setenv("BEACON_CLOUD_S3_BUCKET", "bucket")
	t.Setenv("BEACON_CLOUD_S3_PREFIX", "prefix")
	t.Setenv("BEACON_CLOUD_S3_REGION", "us-west-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "token")
	t.Setenv("BEACON_RUN_ID", "run")

	cfg := ConfigFromEnv()
	if cfg.Upload != uploadS3 {
		t.Fatalf("Upload = %q, want %q", cfg.Upload, uploadS3)
	}
	if cfg.Bucket != "bucket" || cfg.Prefix != "prefix" || cfg.S3Region != "us-west-2" {
		t.Fatalf("unexpected S3 config: %#v", cfg)
	}
	if cfg.S3AccessKeyID != "AKIATEST" || cfg.S3SecretKey != "secret" || cfg.S3SessionToken != "token" {
		t.Fatalf("unexpected S3 credentials in config: %#v", cfg)
	}
}

func TestConfigFromEnvDefaultsToGCSWhenS3BucketIsAlsoSet(t *testing.T) {
	t.Setenv("BEACON_CLOUD_UPLOAD", "")
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "gcs-bucket")
	t.Setenv("BEACON_CLOUD_GCS_PREFIX", "gcs-prefix")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", "gcs-credentials")
	t.Setenv("BEACON_CLOUD_S3_BUCKET", "s3-bucket")

	cfg := ConfigFromEnv()
	if cfg.Upload != uploadGCS {
		t.Fatalf("Upload = %q, want %q", cfg.Upload, uploadGCS)
	}
	if cfg.Bucket != "gcs-bucket" || cfg.Prefix != "gcs-prefix" || cfg.CredentialsB64 != "gcs-credentials" {
		t.Fatalf("unexpected GCS config: %#v", cfg)
	}
}

func TestUploadSendsJSONLToS3(t *testing.T) {
	var uploadedPath, uploadedAuth, uploadedDate, uploadedHash, uploadedToken, uploadedType, uploadedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadedPath = r.URL.EscapedPath()
		uploadedAuth = r.Header.Get("Authorization")
		uploadedDate = r.Header.Get("X-Amz-Date")
		uploadedHash = r.Header.Get("X-Amz-Content-Sha256")
		uploadedToken = r.Header.Get("X-Amz-Security-Token")
		uploadedType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		uploadedBody = string(data)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	body := "{\"event\":\"ok\"}\n"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte(body), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cfg := Config{
		Upload:         uploadS3,
		LogPath:        logPath,
		StatePath:      filepath.Join(t.TempDir(), "state.json"),
		Bucket:         "bucket",
		Prefix:         "prefix",
		Provider:       "claude_code_web",
		UserID:         "user",
		RunID:          "run",
		S3Region:       "us-west-2",
		S3AccessKeyID:  "AKIATEST",
		S3SecretKey:    "secret",
		S3SessionToken: "session-token",
		S3Endpoint:     server.URL,
	}
	if err := Upload(context.Background(), cfg, true); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if !strings.Contains(uploadedPath, "/bucket/prefix/provider=claude_code_web/user_id=user/run_id=run/runtime.jsonl") {
		t.Fatalf("upload path = %q", uploadedPath)
	}
	if !strings.HasPrefix(uploadedAuth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
		t.Fatalf("Authorization = %q", uploadedAuth)
	}
	for _, want := range []string{"us-west-2/s3/aws4_request", "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token"} {
		if !strings.Contains(uploadedAuth, want) {
			t.Fatalf("Authorization missing %q: %q", want, uploadedAuth)
		}
	}
	if uploadedDate == "" {
		t.Fatal("X-Amz-Date is empty")
	}
	sum := sha256.Sum256([]byte(body))
	if uploadedHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("X-Amz-Content-Sha256 = %q", uploadedHash)
	}
	if uploadedToken != "session-token" {
		t.Fatalf("X-Amz-Security-Token = %q", uploadedToken)
	}
	if uploadedType != contentTypeJSONL {
		t.Fatalf("Content-Type = %q, want %q", uploadedType, contentTypeJSONL)
	}
	if uploadedBody != body {
		t.Fatalf("uploaded body = %q", uploadedBody)
	}
}

func TestResolveRunIDUsesOnlyEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_REMOTE_SESSION_ID", "cse_env")
	if got := resolveRunID(); got != "cse_env" {
		t.Fatalf("resolveRunID = %q, want cse_env", got)
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
