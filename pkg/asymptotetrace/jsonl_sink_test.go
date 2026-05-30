package asymptotetrace

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLSinkWritesOneEnvelopePerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.jsonl")
	sink := NewJSONLSink(path)

	envelopes := []Envelope{testEnvelope("one"), testEnvelope("two")}
	if err := sink.WriteBatch(context.Background(), envelopes); err != nil {
		t.Fatalf("WriteBatch returned error: %v", err)
	}

	lines := readJSONLLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		var envelope Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("line is not an envelope JSON object: %v line=%q", err, line)
		}
		if envelope.Vendor != Vendor && envelope.Vendor != "" {
			t.Fatalf("unexpected vendor: %#v", envelope)
		}
	}
}

func TestJSONLSinkAppendsWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	sink := NewJSONLSink(path)

	if err := sink.WriteBatch(context.Background(), []Envelope{testEnvelope("one")}); err != nil {
		t.Fatalf("first WriteBatch returned error: %v", err)
	}
	if err := sink.WriteBatch(context.Background(), []Envelope{testEnvelope("two")}); err != nil {
		t.Fatalf("second WriteBatch returned error: %v", err)
	}

	if lines := readJSONLLines(t, path); len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
}

func TestJSONLSinkRejectsDirectoryPath(t *testing.T) {
	sink := NewJSONLSink(t.TempDir())
	if err := sink.WriteBatch(context.Background(), []Envelope{testEnvelope("one")}); err == nil {
		t.Fatal("WriteBatch returned nil for directory path")
	}
}

func TestJSONLSinkSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	sink := NewJSONLSink(path)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sink.WriteBatch(context.Background(), []Envelope{testEnvelope("concurrent")}); err != nil {
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
		var envelope Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("line is not JSON: %v line=%q", err, line)
		}
	}
}

func TestFlushIntervalWritesBatch(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10, FlushInterval: 10 * time.Millisecond})
	defer client.Close(context.Background())

	if _, err := client.Emit(testEnvelope("interval")); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	deadline := time.After(time.Second)
	for {
		if len(sink.envelopes()) == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("flush interval did not write batch")
		case <-time.After(10 * time.Millisecond):
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
