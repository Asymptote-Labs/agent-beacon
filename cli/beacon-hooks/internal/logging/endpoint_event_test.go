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
	if err := logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil); err != nil {
		t.Fatalf("EndpointEvent returned error: %v", err)
	}

	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("expected structured endpoint event, len=%d err=%v", len(data), err)
	}
}

func TestEndpointEventRotatesRuntimeLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(logPath, []byte("old log contents"), 0644); err != nil {
		t.Fatalf("write existing log: %v", err)
	}

	if err := appendEndpointJSONL(logPath, []byte("{\"message\":\"new event\"}\n"), 1, 2); err != nil {
		t.Fatalf("appendEndpointJSONL returned error: %v", err)
	}

	if rotated, err := os.ReadFile(logPath + ".1"); err != nil || string(rotated) != "old log contents" {
		t.Fatalf("expected rotated archive, data=%q err=%v", string(rotated), err)
	}
	if current, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(current), "new event") {
		t.Fatalf("expected current log to contain new event, data=%q err=%v", string(current), err)
	}
}

func TestEndpointEventSurfacesWriteFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	if err := os.Mkdir(logPath, 0755); err != nil {
		t.Fatalf("mkdir log path: %v", err)
	}
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	logger := NewLoggerForPlatform("pre-tool", "test")
	if err := logger.EndpointEvent("approval.allowed", "approval", "info", "Pre-tool observed", nil); err == nil {
		t.Fatal("EndpointEvent returned nil, want write failure")
	}
}
