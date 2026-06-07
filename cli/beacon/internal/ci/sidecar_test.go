package ci

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
)

func TestStartDetachedAndStopDetached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX signal semantics")
	}
	collector := fakeExecutable(t, "collector", "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n")
	oldWait := waitCollectorReady
	waitCollectorReady = func(endpointconfig.Config, time.Duration) error { return nil }
	t.Cleanup(func() { waitCollectorReady = oldWait })

	dir := t.TempDir()
	session := &Session{
		CollectorBinary: collector,
		ConfigPath:      filepath.Join(dir, "otelcol.yaml"),
		cfg:             endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	if err := os.WriteFile(session.ConfigPath, []byte("receivers: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	pid, err := session.StartDetached(nil)
	if err != nil {
		t.Fatalf("StartDetached returned error: %v", err)
	}
	if pid <= 0 || session.PID != pid {
		t.Fatalf("unexpected pid: returned=%d session=%d", pid, session.PID)
	}
	if !processAlive(pid) {
		t.Fatal("collector should be alive after StartDetached")
	}

	if err := session.StopDetached(3 * time.Second); err != nil {
		t.Fatalf("StopDetached returned error: %v", err)
	}
	// Give the kernel a moment to reap before the final liveness check.
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("collector should be stopped after StopDetached")
	}
}

func TestStartDetachedReadyFailureReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uses POSIX signal semantics")
	}
	collector := fakeExecutable(t, "collector", "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n")
	oldWait := waitCollectorReady
	waitCollectorReady = func(endpointconfig.Config, time.Duration) error {
		return errors.New("collector not ready")
	}
	t.Cleanup(func() { waitCollectorReady = oldWait })

	dir := t.TempDir()
	session := &Session{
		CollectorBinary: collector,
		ConfigPath:      filepath.Join(dir, "otelcol.yaml"),
		cfg:             endpointconfig.Default(true, filepath.Join(dir, "runtime.jsonl")),
	}
	if err := os.WriteFile(session.ConfigPath, []byte("receivers: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := session.StartDetached(nil); err == nil {
		t.Fatal("StartDetached should fail when the collector never becomes ready")
	}
	if session.PID != 0 {
		t.Fatalf("PID should be reset after a failed start, got %d", session.PID)
	}
}

func TestWriteAndLoadSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, StateFileName)
	original := &Session{
		BaseDir:         dir,
		LogPath:         filepath.Join(dir, "runtime.jsonl"),
		ConfigPath:      filepath.Join(dir, "otelcol.yaml"),
		GRPCEndpoint:    "http://127.0.0.1:4317",
		Harness:         DefaultHarness,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
		Forward:         ForwardSplunk,
		ForwardEndpoint: "https://splunk.example/services/collector",
		PID:             4242,
	}
	if err := original.WriteState(statePath); err != nil {
		t.Fatalf("WriteState returned error: %v", err)
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Fatalf("state file must not reference a token:\n%s", raw)
	}

	loaded, err := LoadSession(statePath)
	if err != nil {
		t.Fatalf("LoadSession returned error: %v", err)
	}
	if loaded.PID != original.PID || loaded.LogPath != original.LogPath ||
		loaded.Harness != original.Harness || loaded.Forward != original.Forward ||
		loaded.ForwardEndpoint != original.ForwardEndpoint {
		t.Fatalf("round-trip mismatch: %+v vs %+v", loaded, original)
	}
	if loaded.StartedAtTime().IsZero() {
		t.Fatal("StartedAtTime should parse from the restored StartedAt")
	}
}
