package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRedactionConfig(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"user_mode":true,"redaction":{"prompt_mode":"` + mode + `"}}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func promptEvent(t *testing.T, logPath string) map[string]interface{} {
	t.Helper()
	logger := NewLoggerForPlatform("prompt-submit", "claude")
	fields := map[string]interface{}{"prompt": map[string]interface{}{"text": "deploy api_key=hunter2"}}
	if err := logger.EndpointEvent("prompt.submitted", "prompt", "info", "Prompt submitted", fields); err != nil {
		t.Fatalf("EndpointEvent: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &event); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return event
}

func TestPromptRetentionFullKeepsBody(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ENDPOINT_CONFIG", writeRedactionConfig(t, "full"))

	event := promptEvent(t, logPath)
	prompt := event["prompt"].(map[string]interface{})
	// Secret redaction still applies as a floor, but the prompt body is retained.
	if got := prompt["text"]; got != "deploy api_key=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want secret-redacted body", got)
	}
	if _, ok := prompt["hash"]; ok {
		t.Fatalf("full mode should not add a digest: %#v", prompt)
	}
	if _, ok := event["content"]; ok {
		t.Fatalf("full mode should not add a content marker: %#v", event["content"])
	}
}

func TestPromptRetentionRedactedReplacesBody(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ENDPOINT_CONFIG", writeRedactionConfig(t, "redacted"))

	event := promptEvent(t, logPath)
	prompt := event["prompt"].(map[string]interface{})
	if got := prompt["text"]; got != "[REDACTED]" {
		t.Fatalf("prompt.text = %q, want placeholder", got)
	}
	if prompt["hash"] == nil || prompt["hash"] == "" {
		t.Fatalf("redacted mode should keep a digest hash: %#v", prompt)
	}
	content := event["content"].(map[string]interface{})
	if content["retention"] != "redacted" || content["included"] != true || content["redacted"] != true {
		t.Fatalf("content marker = %#v", content)
	}
	if strings.Contains(string(mustMarshal(t, event)), "hunter2") {
		t.Fatalf("redacted mode leaked prompt body")
	}
}

func TestPromptRetentionMetadataDropsBody(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_ENDPOINT_CONFIG", writeRedactionConfig(t, "metadata"))

	event := promptEvent(t, logPath)
	prompt := event["prompt"].(map[string]interface{})
	if _, ok := prompt["text"]; ok {
		t.Fatalf("metadata mode should drop the body: %#v", prompt)
	}
	if prompt["hash"] == nil || prompt["length"] == nil {
		t.Fatalf("metadata mode should keep a digest: %#v", prompt)
	}
	content := event["content"].(map[string]interface{})
	if content["retention"] != "metadata" || content["included"] != false {
		t.Fatalf("content marker = %#v", content)
	}
}

func TestPromptRetentionDefaultsToFullWithoutConfig(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	// No BEACON_ENDPOINT_CONFIG set.

	event := promptEvent(t, logPath)
	prompt := event["prompt"].(map[string]interface{})
	if got := prompt["text"]; got != "deploy api_key=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want retained body when no config", got)
	}
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
