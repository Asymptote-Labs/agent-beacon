package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointRedaction(t *testing.T) {
	got := redactEndpointString("token=super-secret")
	if got == "token=super-secret" {
		t.Fatal("expected token to be redacted")
	}
}

func TestRegularLogDoesNotWriteEndpointEventByDefault(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	logger.Info("diagnostic only")

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("generic logger wrote endpoint event by default, stat err=%v", err)
	}
}

func TestEndpointEventStillWritesStructuredTelemetry(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil)

	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("expected structured endpoint event, len=%d err=%v", len(data), err)
	}
}

func TestEndpointEventRotatesRuntimeLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	if err := os.WriteFile(logPath, []byte(strings.Repeat("old", 4*1024*1024)), 0644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}

	logger := NewLoggerForPlatform("pre-tool", "test")
	logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil)

	if rotated, err := os.ReadFile(logPath + ".1"); err != nil || len(rotated) == 0 {
		t.Fatalf("expected rotated archive, len=%d err=%v", len(rotated), err)
	}
	if current, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(current), "Pre-tool observed") {
		t.Fatalf("expected current log to contain new event, data=%q err=%v", string(current), err)
	}
}
