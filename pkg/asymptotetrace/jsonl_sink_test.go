package asymptotetrace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJSONLSinkWritesCanonicalEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	sink := NewJSONLSink(path)

	events := []Event{
		NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}}),
		NewEvent(NewEventOptions{Action: "command.executed", Harness: HarnessInfo{Name: "codex"}}),
	}
	if err := sink.WriteBatch(context.Background(), events); err != nil {
		t.Fatalf("WriteBatch returned error: %v", err)
	}

	lines := readJSONLLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not canonical event JSON: %v line=%q", err, line)
		}
		if event.Vendor != Vendor || event.Product != Product || event.SchemaVersion != SchemaVersion {
			t.Fatalf("unexpected event identity: %#v", event)
		}
	}
}

func TestJSONLSinkAppendsWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	sink := NewJSONLSink(path)

	if err := sink.WriteBatch(context.Background(), []Event{NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})}); err != nil {
		t.Fatalf("first WriteBatch returned error: %v", err)
	}
	if err := sink.WriteBatch(context.Background(), []Event{NewEvent(NewEventOptions{Action: "tool.completed", Harness: HarnessInfo{Name: "cursor"}})}); err != nil {
		t.Fatalf("second WriteBatch returned error: %v", err)
	}
	if lines := readJSONLLines(t, path); len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
}

func TestJSONLSinkRejectsDirectoryPath(t *testing.T) {
	sink := NewJSONLSink(t.TempDir())
	if err := sink.WriteBatch(context.Background(), []Event{NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})}); err == nil {
		t.Fatal("WriteBatch returned nil for directory path")
	}
}

func TestJSONLSinkRejectsEmptyPath(t *testing.T) {
	sink := NewJSONLSink("")
	err := sink.WriteBatch(context.Background(), []Event{NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})})
	if !errors.Is(err, ErrSinkPathRequired) {
		t.Fatalf("WriteBatch error = %v, want ErrSinkPathRequired", err)
	}
}

func TestJSONLSinkSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	sink := NewJSONLSink(path)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := sink.WriteBatch(context.Background(), []Event{NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})})
			if err != nil {
				t.Errorf("WriteBatch returned error: %v", err)
			}
		}()
	}
	wg.Wait()

	lines := readJSONLLines(t, path)
	if len(lines) != 20 {
		t.Fatalf("lines = %d, want 20", len(lines))
	}
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not JSON: %v line=%q", err, line)
		}
	}
}

func readJSONLLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open JSONL: %v", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	return lines
}
