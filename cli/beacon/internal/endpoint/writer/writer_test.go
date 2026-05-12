package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestAppendEventWritesSingleJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "test event",
	})
	written, err := AppendEvent(event, Options{Path: path})
	if err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	if written != path {
		t.Fatalf("expected path %q, got %q", path, written)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if decoded["vendor"] != schema.Vendor {
		t.Fatalf("unexpected vendor: %v", decoded["vendor"])
	}
}

func TestAppendEventRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "token=super-secret-value",
	})
	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "super-secret-value") {
		t.Fatalf("secret was not redacted: %s", string(data))
	}
}

func TestAppendEventRedactsNestedSlices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "nested event",
	})
	event.Raw = map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"value": "authorization: Bearer secret-token"},
		},
	}
	if _, err := AppendEvent(event, Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "secret-token") {
		t.Fatalf("nested secret was not redacted: %s", string(data))
	}
}

func TestAppendEventRotatesExistingLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.jsonl")
	if err := os.WriteFile(path, []byte("old log contents"), 0644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "agent.detected",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "new event",
	})

	if _, err := AppendEvent(event, Options{Path: path, RotateSize: 1}); err != nil {
		t.Fatalf("AppendEvent returned error: %v", err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if string(rotated) != "old log contents" {
		t.Fatalf("rotated log = %q", string(rotated))
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if !strings.Contains(string(current), "new event") {
		t.Fatalf("current log missing new event: %s", string(current))
	}
}

func TestLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if _, err := LastLine(path); err == nil {
		t.Fatal("expected missing file error")
	}
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	last, err := LastLine(path)
	if err != nil {
		t.Fatalf("LastLine returned error: %v", err)
	}
	if last != "second" {
		t.Fatalf("LastLine = %q, want second", last)
	}
}

func TestAppendEventRejectsInvalidEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
	})
	event.Event.Action = ""

	if _, err := AppendEvent(event, Options{Path: path}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestAppendEventRejectsOversizedEventAfterTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: strings.Repeat("h", 2048)},
		Message: strings.Repeat("m", 2048),
	})

	if _, err := AppendEvent(event, Options{Path: path, MaxBytes: 512}); err == nil {
		t.Fatal("expected oversized event error")
	}
}

func TestSanitizeEventRedactsToolPolicyAndRawValues(t *testing.T) {
	event := schema.NewEvent(schema.NewEventOptions{
		Action:  "tool.invoked",
		Harness: schema.HarnessInfo{Name: "test"},
		Message: "authorization=Bearer message-secret",
	})
	event.Tool = &schema.ToolInfo{
		Command: "curl -H 'Authorization: Bearer command-secret'",
		Path:    strings.Repeat("p", 3000),
	}
	event.Policy = &schema.PolicyInfo{Reason: "api_key=policy-secret"}
	event.Raw = map[string]interface{}{
		"nested": map[string]interface{}{"token": "token=raw-secret"},
	}

	sanitized := SanitizeEvent(event, MaxEventBytes)
	data, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatalf("marshal sanitized event: %v", err)
	}
	text := string(data)
	for _, secret := range []string{"message-secret", "command-secret", "policy-secret", "raw-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted: %s", secret, text)
		}
	}
	if len(sanitized.Tool.Path) > 2048 {
		t.Fatalf("tool path was not truncated: %d", len(sanitized.Tool.Path))
	}
}
