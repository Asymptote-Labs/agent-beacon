package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestValidateRequiresStructuredHarnessEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "claude_code"
	writeEventLine(t, path, event)

	result := Validate(ValidationOptions{LogPath: path, MinEvents: 1, RequireHarness: "claude"})
	if result.Status != "ok" {
		t.Fatalf("Validate status = %q, stages=%#v", result.Status, result.Stages)
	}
	if result.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1", result.EventCount)
	}
}

func TestValidateFailsOnMalformedJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result := Validate(ValidationOptions{LogPath: path, MinEvents: 1})
	if result.Status != "fail" {
		t.Fatalf("Validate status = %q, want fail", result.Status)
	}
	if len(result.Stages) < 2 || result.Stages[1].Name != "runtime_log_parseable" {
		t.Fatalf("unexpected stages: %#v", result.Stages)
	}
}

func TestValidateFailsWhenHarnessEventMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "codex_cli"
	writeEventLine(t, path, event)

	result := Validate(ValidationOptions{LogPath: path, MinEvents: 1, RequireHarness: "claude"})
	if result.Status != "fail" {
		t.Fatalf("Validate status = %q, want fail", result.Status)
	}
	if result.EventCount != 0 {
		t.Fatalf("EventCount = %d, want 0", result.EventCount)
	}
}

func TestValidateMatchesAnyConfiguredHarness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "codex_cli"
	writeEventLine(t, path, event)

	result := Validate(ValidationOptions{LogPath: path, MinEvents: 1, RequireHarness: "claude,codex"})
	if result.Status != "ok" {
		t.Fatalf("Validate status = %q, stages=%#v", result.Status, result.Stages)
	}
	if result.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1", result.EventCount)
	}
}

func TestValidateMatchesCodexAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "codex_cli"
	writeEventLine(t, path, event)

	result := Validate(ValidationOptions{LogPath: path, MinEvents: 1, RequireHarness: "codex"})
	if result.Status != "ok" {
		t.Fatalf("Validate status = %q, stages=%#v", result.Status, result.Stages)
	}
}

func TestValidateFiltersEventsBeforeSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "claude_code"
	event.Timestamp = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	writeEventLine(t, path, event)

	result := Validate(ValidationOptions{
		LogPath:        path,
		MinEvents:      1,
		RequireHarness: "claude",
		Since:          time.Now().Add(-1 * time.Minute),
	})
	if result.Status != "fail" {
		t.Fatalf("Validate status = %q, want fail for stale event", result.Status)
	}
	if result.EventCount != 0 {
		t.Fatalf("EventCount = %d, want 0", result.EventCount)
	}
}

func TestValidateSinceIgnoresOldMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	// Write an old malformed line followed by a valid new event.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		t.Fatal(err)
	}
	event := NewSessionEvent("ci.test", "test event", nil)
	event.Harness.Name = "claude_code"
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	result := Validate(ValidationOptions{
		LogPath:        path,
		MinEvents:      1,
		RequireHarness: "claude",
		Since:          time.Now().Add(-1 * time.Minute),
	})
	if result.Status != "ok" {
		t.Fatalf("Validate status = %q, want ok; old malformed line should be ignored with Since filter; stages=%#v", result.Status, result.Stages)
	}
	if result.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1", result.EventCount)
	}
}

func writeEventLine(t *testing.T, path string, event schema.Event) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
