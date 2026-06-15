package asymptotetrace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingSink struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	events  []Event
}

func newBlockingSink() *blockingSink {
	return &blockingSink{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingSink) WriteBatch(ctx context.Context, events []Event) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		s.events = append(s.events, copyEvent(event))
	}
	return nil
}

func (s *blockingSink) Flush(context.Context) error { return nil }
func (s *blockingSink) Close() error                { return nil }

func (s *blockingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]Event, len(s.events))
	copy(events, s.events)
	return events
}

type firstWriteBlockingSink struct {
	started  chan struct{}
	release  chan struct{}
	flushErr error

	once       sync.Once
	mu         sync.Mutex
	flushCount int
}

func newFirstWriteBlockingSink(flushErr error) *firstWriteBlockingSink {
	return &firstWriteBlockingSink{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		flushErr: flushErr,
	}
}

func (s *firstWriteBlockingSink) WriteBatch(ctx context.Context, events []Event) error {
	block := false
	s.once.Do(func() {
		block = true
		close(s.started)
	})
	if block {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *firstWriteBlockingSink) Flush(context.Context) error {
	s.mu.Lock()
	s.flushCount++
	s.mu.Unlock()
	return s.flushErr
}

func (s *firstWriteBlockingSink) Close() error {
	return nil
}

func (s *firstWriteBlockingSink) flushes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushCount
}

func TestStartRejectsMissingHarness(t *testing.T) {
	if _, err := Start(Options{}); !errors.Is(err, ErrHarnessRequired) {
		t.Fatalf("Start error = %v, want ErrHarnessRequired", err)
	}
}

func TestStartDefaultsAndShutdownWritesJSONL(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	tracer, err := Start(Options{Harness: "my-agent", Path: path})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	result, err := tracer.Observe(context.Background(), Capture{
		Action:   "runner.started",
		Category: "runner",
		Raw:      map[string]interface{}{"input": "hello"},
	})
	if err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	if !result.Accepted || result.Dropped {
		t.Fatalf("Observe result = %#v, want accepted", result)
	}
	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	events := readJSONLEvents(t, path)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Harness.Name != "my-agent" || events[0].Origin != OriginLocal {
		t.Fatalf("unexpected harness/origin: %#v", events[0])
	}
	if events[0].Raw["input"] != "hello" {
		t.Fatalf("raw input = %#v, want hello", events[0].Raw)
	}
}

func TestStartDefaultsToLocalOriginAndDefaultJSONLSink(t *testing.T) {
	tracer, err := Start(Options{Harness: "my-agent"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer tracer.Shutdown(context.Background())

	if tracer.opts.Origin != OriginLocal {
		t.Fatalf("Origin = %q, want local", tracer.opts.Origin)
	}
	sink, ok := tracer.opts.Sink.(*JSONLSink)
	if !ok {
		t.Fatalf("Sink = %T, want *JSONLSink", tracer.opts.Sink)
	}
	if sink.path != DefaultTracePath {
		t.Fatalf("default path = %q, want %q", sink.path, DefaultTracePath)
	}
}

func TestObserveDoesNotCallSinkSynchronously(t *testing.T) {
	sink := newBlockingSink()
	tracer, err := Start(Options{Harness: "my-agent", Sink: sink, BufferSize: 4})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer func() {
		close(sink.release)
		_ = tracer.Shutdown(context.Background())
	}()

	done := make(chan error, 1)
	go func() {
		_, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Observe returned error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Observe blocked on sink")
	}
}

func TestFlushDrainsQueuedCaptures(t *testing.T) {
	sink := &captureEventSink{}
	tracer, err := Start(Options{Harness: "my-agent", Sink: sink, BufferSize: 10})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer tracer.Shutdown(context.Background())

	for i := 0; i < 3; i++ {
		if _, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"}); err != nil {
			t.Fatalf("Observe returned error: %v", err)
		}
	}
	if err := tracer.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	events, _, _ := sink.snapshot()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
}

func TestConcurrentFlushBlockedOnFullBufferIsAckedDuringShutdown(t *testing.T) {
	want := errors.New("flush barrier ran")
	sink := newFirstWriteBlockingSink(want)
	tracer, err := Start(Options{Harness: "my-agent", Sink: sink, BufferSize: 1})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if _, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"}); err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}
	if _, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"}); err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}

	flushDone := make(chan error, 1)
	go func() {
		flushDone <- tracer.Flush(context.Background())
	}()
	for deadline := time.After(time.Second); tracer.pendingSends.Load() == 0; {
		select {
		case <-deadline:
			t.Fatal("Flush did not block on the full command buffer")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- tracer.Shutdown(context.Background())
	}()
	close(sink.release)

	select {
	case err := <-flushDone:
		if !errors.Is(err, want) {
			t.Fatalf("Flush error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("Flush was not acked during shutdown")
	}
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, want) {
			t.Fatalf("Shutdown error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	if sink.flushes() == 0 {
		t.Fatal("sink Flush was not called")
	}
}

func TestObserveDropsWhenBufferFull(t *testing.T) {
	sink := newBlockingSink()
	tracer, err := Start(Options{Harness: "my-agent", Sink: sink, BufferSize: 1})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	defer func() {
		close(sink.release)
		_ = tracer.Shutdown(context.Background())
	}()

	if _, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"}); err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}
	var dropped int
	for i := 0; i < 100; i++ {
		result, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"})
		if err != nil && !errors.Is(err, ErrTracerClosed) {
			t.Fatalf("Observe returned error: %v", err)
		}
		if result.Dropped {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected at least one dropped capture")
	}
	if stats := tracer.Stats(); stats.Dropped == 0 {
		t.Fatalf("Dropped stats = 0, want drops: %#v", stats)
	}
}

func readJSONLEvents(t *testing.T, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal JSONL event: %v line=%q", err, line)
		}
		events = append(events, event)
	}
	return events
}

func TestTracerPrivacyMetadataRemovesInputOutput(t *testing.T) {
	sink := &captureEventSink{}
	tracer, err := Start(Options{
		Harness: "my-agent",
		Sink:    sink,
		Privacy: &PrivacyPolicy{Retention: ContentRetentionMetadata},
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	_, err = tracer.Observe(context.Background(), Capture{
		Action:   "runner.completed",
		Category: "runner",
		Input:    "SECRET input",
		Output:   "SECRET output",
	})
	if err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	events, _, _ := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	rawText := stringify(events[0].Raw)
	if strings.Contains(rawText, "SECRET") {
		t.Fatalf("metadata privacy leaked input/output: %#v", events[0].Raw)
	}
	if events[0].Raw["field_count"] != 2 {
		t.Fatalf("metadata raw = %#v, want field_count 2", events[0].Raw)
	}
}

func TestTracerRecordsSinkErrorsInStats(t *testing.T) {
	sink := &captureEventSink{writeErr: errors.New("write failed")}
	tracer, err := Start(Options{Harness: "my-agent", Sink: sink})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if _, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"}); err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	if err := tracer.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	stats := tracer.Stats()
	if stats.Errors == 0 || !strings.Contains(stats.LastError, "write failed") {
		t.Fatalf("stats did not record sink error: %#v", stats)
	}
}

func TestObserveAfterShutdownReturnsClosed(t *testing.T) {
	tracer, err := Start(Options{Harness: "my-agent", Sink: NoopSink{}})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	result, err := tracer.Observe(context.Background(), Capture{Action: "runner.started"})
	if !errors.Is(err, ErrTracerClosed) {
		t.Fatalf("Observe error = %v, want ErrTracerClosed", err)
	}
	if !result.Dropped || result.Accepted {
		t.Fatalf("Observe result = %#v, want dropped", result)
	}
}

func TestConcurrentObserveIsRaceSafe(t *testing.T) {
	tracer, err := Start(Options{Harness: "my-agent", Sink: NoopSink{}, BufferSize: 2048})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = tracer.Observe(context.Background(), Capture{Action: "runner.started"})
			}
		}()
	}
	wg.Wait()
	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	stats := tracer.Stats()
	if stats.Accepted+stats.Dropped != 1000 {
		t.Fatalf("accepted+dropped = %d, want 1000: %#v", stats.Accepted+stats.Dropped, stats)
	}
}
