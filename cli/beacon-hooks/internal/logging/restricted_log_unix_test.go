//go:build unix

package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// In restricted log mode the hook never opens the log: it sends the finished line to the root
// collector over the socket the marker names, and the collector appends it.
func TestEndpointEventSendsToTheCollectorInRestrictedMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	socketDir, err := os.MkdirTemp("", "bhsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "hooks.sock")
	t.Cleanup(asymptoteobserve.UseRestrictedLogMarkerForTest(filepath.Join(dir, "log-access.json")))
	if err := asymptoteobserve.WriteRestrictedLogMarker(asymptoteobserve.RestrictedLog{LogPath: logPath, Socket: socket, Group: asymptoteobserve.RuntimeLogGroup, GID: 4242}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var received [][]byte
	server, err := asymptoteobserve.ListenRuntimeLogSocket(socket, func(line []byte) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, append([]byte{}, line...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	logger := NewLoggerForPlatform("prompt", "claude")
	if err := logger.EndpointEvent("prompt.submitted", "prompt", "info", "Prompt submitted", map[string]interface{}{"prompt": map[string]interface{}{"text": "hello"}}); err != nil {
		t.Fatalf("EndpointEvent: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("collector received %d lines, want 1", len(received))
	}
	var event map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(received[0]), &event); err != nil {
		t.Fatalf("received line is not JSON: %v", err)
	}
	if id, _ := event["event"].(map[string]interface{})["id"].(string); id == "" {
		t.Error("the sent line carries no event.id; the hook must stamp it before sending")
	}
	if _, err := os.Stat(filepath.Dir(logPath)); !os.IsNotExist(err) {
		t.Errorf("the hook touched the restricted log directory: %v", err)
	}
}

// With no collector listening the event is lost, and the hook says so with the restricted-mode
// remedy rather than suggesting a file permission the user cannot change.
func TestEndpointEventReportsAMissingCollectorInRestrictedMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Cleanup(asymptoteobserve.UseRestrictedLogMarkerForTest(filepath.Join(dir, "log-access.json")))
	if err := asymptoteobserve.WriteRestrictedLogMarker(asymptoteobserve.RestrictedLog{LogPath: logPath, Socket: filepath.Join(dir, "absent.sock"), Group: asymptoteobserve.RuntimeLogGroup, GID: 4242}); err != nil {
		t.Fatal(err)
	}
	logger := NewLoggerForPlatform("prompt", "claude")
	if err := logger.EndpointEvent("prompt.submitted", "prompt", "info", "Prompt submitted", nil); err == nil {
		t.Fatal("EndpointEvent reported success with no collector listening")
	}
	var stderr bytes.Buffer
	reportedWriteFailures.Delete(logPath)
	reportEndpointWriteFailure(&stderr, "claude", logPath, os.ErrNotExist)
	if !strings.Contains(stderr.String(), "restricted mode") {
		t.Errorf("failure report = %q, want the restricted-mode remedy", stderr.String())
	}
}
