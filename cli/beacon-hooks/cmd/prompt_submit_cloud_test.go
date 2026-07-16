package cmd

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPromptSubmitUploadsCursorCloudTelemetryToS3(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "cursor"

	uploaded := make(chan []byte, 1)
	var uploadPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", r.Header.Get("Content-Encoding"))
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("Authorization = %q, want SigV4", r.Header.Get("Authorization"))
		}
		uploadPath = r.URL.Path
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("open gzip body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Errorf("read gzip body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uploaded <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CLOUD_SHUTTLE_STATE", filepath.Join(tempDir, "shuttle-state.json"))
	t.Setenv("BEACON_ORIGIN", "cloud")
	t.Setenv("BEACON_RUN_PROVIDER", "cursor_cloud")
	t.Setenv("BEACON_RUN_ID", "")
	t.Setenv("BEACON_CLOUD_UPLOAD", "s3")
	t.Setenv("BEACON_CLOUD_S3_BUCKET", "telemetry-bucket")
	t.Setenv("BEACON_CLOUD_S3_PREFIX", "agent-traces")
	t.Setenv("BEACON_CLOUD_S3_REGION", "us-east-1")
	t.Setenv("BEACON_CLOUD_S3_ENDPOINT", server.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")

	out := runHookWithInput(t, runPromptSubmit, map[string]interface{}{
		"conversation_id": "conv-prompt",
		"hook_event_name": "beforeSubmitPrompt",
		"prompt":          "Summarize this repository",
		"cwd":             "/workspace",
	})
	if out["continue"] != true {
		t.Fatalf("prompt response = %#v, want continue=true", out)
	}

	body := <-uploaded
	var event map[string]interface{}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("unmarshal uploaded event: %v\n%s", err, body)
	}
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if prompt := event["prompt"].(map[string]interface{})["text"]; prompt != "Summarize this repository" {
		t.Fatalf("prompt.text = %q", prompt)
	}
	run := event["run"].(map[string]interface{})
	if run["provider"] != "cursor_cloud" || run["run_id"] != "conv-prompt" {
		t.Fatalf("run = %#v", run)
	}
	if !strings.Contains(uploadPath, "/telemetry-bucket/agent-traces/runtime/") ||
		!strings.HasSuffix(uploadPath, "-cursor_cloud-conv-prompt.jsonl.gz") {
		t.Fatalf("upload path = %q", uploadPath)
	}
}
