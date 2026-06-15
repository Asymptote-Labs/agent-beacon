package devincloud

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
	"strings"
	"testing"
)

func TestGCSUploaderSignsJWTAndPuts(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))

	var gotPath, gotAuth, gotType, gotBody string
	var tokenForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			tokenForm = r.Form.Encode()
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "tkn", "expires_in": 3600})
		default:
			gotPath = r.URL.EscapedPath()
			gotAuth = r.Header.Get("Authorization")
			gotType = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	creds := serviceAccount{
		ClientEmail: "beacon@example.iam.gserviceaccount.com",
		PrivateKey:  keyPEM,
		TokenURI:    srv.URL + "/token",
	}
	credsJSON, _ := json.Marshal(creds)

	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "bucket")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", base64.StdEncoding.EncodeToString(credsJSON))
	t.Setenv("BEACON_CLOUD_GCS_ENDPOINT", srv.URL)

	up, ok, err := NewGCSUploaderFromEnv()
	if err != nil || !ok {
		t.Fatalf("NewGCSUploaderFromEnv ok=%v err=%v", ok, err)
	}

	obj := ObjectName("agent-traces", Provider, "user-1", "sess-1")
	if err := up.Upload(context.Background(), obj, []byte("{\"event\":\"ok\"}\n")); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if !strings.Contains(tokenForm, "grant_type=urn") || !strings.Contains(tokenForm, "assertion=") {
		t.Fatalf("token form unexpected: %s", tokenForm)
	}
	if gotAuth != "Bearer tkn" {
		t.Fatalf("auth = %q, want Bearer tkn", gotAuth)
	}
	if gotType != uploadContentType {
		t.Fatalf("content-type = %q, want %q", gotType, uploadContentType)
	}
	if !strings.Contains(gotPath, "/bucket/agent-traces/provider=devin_cloud/user_id=user-1/run_id=sess-1/runtime.jsonl") {
		t.Fatalf("upload path = %q", gotPath)
	}
	if gotBody != "{\"event\":\"ok\"}\n" {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestNewGCSUploaderUnconfigured(t *testing.T) {
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", "")
	up, ok, err := NewGCSUploaderFromEnv()
	if up != nil || ok || err != nil {
		t.Fatalf("unconfigured = (%v,%v,%v), want (nil,false,nil)", up, ok, err)
	}
}

func TestNewGCSUploaderPartialConfigErrors(t *testing.T) {
	t.Setenv("BEACON_CLOUD_GCS_BUCKET", "bucket")
	t.Setenv("BEACON_CLOUD_GCS_CREDENTIALS_B64", "")
	if _, _, err := NewGCSUploaderFromEnv(); err == nil {
		t.Fatal("expected error when only bucket is set")
	}
}
