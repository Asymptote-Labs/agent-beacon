package asymptotetrace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type captureSink struct {
	mu           sync.Mutex
	batches      [][]Envelope
	writeErr     error
	flushErr     error
	closeErr     error
	closeCount   int
	writeStarted chan struct{}
	releaseWrite chan struct{}
}

func newCaptureSink() *captureSink {
	return &captureSink{}
}

func newBlockingSink() *captureSink {
	return &captureSink{
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
	}
}

func (s *captureSink) WriteBatch(ctx context.Context, envelopes []Envelope) error {
	if s.writeStarted != nil {
		select {
		case <-s.writeStarted:
		default:
			close(s.writeStarted)
		}
	}
	if s.releaseWrite != nil {
		select {
		case <-s.releaseWrite:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	copied := make([]Envelope, len(envelopes))
	copy(copied, envelopes)
	s.mu.Lock()
	s.batches = append(s.batches, copied)
	s.mu.Unlock()
	return nil
}

func (s *captureSink) Flush(context.Context) error {
	return s.flushErr
}

func (s *captureSink) Close() error {
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
	return s.closeErr
}

func (s *captureSink) envelopes() []Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Envelope
	for _, batch := range s.batches {
		out = append(out, batch...)
	}
	return out
}

func (s *captureSink) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCount
}

func TestStartUsesDefaultOptions(t *testing.T) {
	client := Start(Options{})
	defer client.Close(context.Background())

	if client.opts.QueueSize != DefaultQueueSize {
		t.Fatalf("QueueSize = %d, want %d", client.opts.QueueSize, DefaultQueueSize)
	}
	if client.opts.BatchSize != DefaultBatchSize {
		t.Fatalf("BatchSize = %d, want %d", client.opts.BatchSize, DefaultBatchSize)
	}
	if client.opts.ContentRetention != ContentRetentionFull {
		t.Fatalf("ContentRetention = %q, want full", client.opts.ContentRetention)
	}
}

func TestStartInvalidRetentionFailsClosed(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10, ContentRetention: "metdata"})
	defer client.Close(context.Background())

	if _, err := client.Emit(Envelope{
		Origin:  OriginLocal,
		Harness: HarnessInfo{Name: "custom_agent"},
		Raw:     map[string]interface{}{"prompt": "sensitive"},
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	envelopes := sink.envelopes()
	if len(envelopes) != 1 {
		t.Fatalf("sink envelopes = %d, want 1", len(envelopes))
	}
	if envelopes[0].Raw["field_count"] != 1 {
		t.Fatalf("invalid retention did not fail closed: %#v", envelopes[0].Raw)
	}
}

func TestEmitReturnsAcceptedWhenQueued(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10})
	defer client.Close(context.Background())

	result, err := client.Emit(testEnvelope("one"))
	if err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if !result.Accepted || result.Dropped {
		t.Fatalf("Emit result = %#v, want accepted", result)
	}
	if stats := client.Stats(); stats.Accepted != 1 || stats.Dropped != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestEmitRejectsInvalidEnvelope(t *testing.T) {
	client := Start(Options{})
	defer client.Close(context.Background())

	_, err := client.Emit(Envelope{Origin: "invalid", Harness: HarnessInfo{Name: "test"}})
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("Emit error = %v, want origin validation error", err)
	}
	if stats := client.Stats(); stats.Accepted != 0 || stats.Dropped != 0 {
		t.Fatalf("invalid envelope changed stats: %#v", stats)
	}
}

func TestEmitDoesNotCallSinkSynchronously(t *testing.T) {
	sink := newBlockingSink()
	client := Start(Options{Sink: sink, BatchSize: 1})
	defer func() {
		close(sink.releaseWrite)
		_ = client.Close(context.Background())
	}()

	done := make(chan error, 1)
	go func() {
		_, err := client.Emit(testEnvelope("one"))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Emit returned error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Emit blocked on sink write")
	}
}

func TestSlowSinkDoesNotBlockEmit(t *testing.T) {
	sink := newBlockingSink()
	client := Start(Options{Sink: sink, QueueSize: 4, BatchSize: 1})
	defer func() {
		close(sink.releaseWrite)
		_ = client.Close(context.Background())
	}()

	if _, err := client.Emit(testEnvelope("first")); err != nil {
		t.Fatalf("first Emit returned error: %v", err)
	}
	select {
	case <-sink.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}

	done := make(chan error, 1)
	go func() {
		_, err := client.Emit(testEnvelope("second"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Emit returned error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Emit blocked behind slow sink")
	}
}

func TestEmitDropsWhenQueueFull(t *testing.T) {
	sink := newBlockingSink()
	client := Start(Options{Sink: sink, QueueSize: 1, BatchSize: 1})
	defer func() {
		close(sink.releaseWrite)
		_ = client.Close(context.Background())
	}()

	if _, err := client.Emit(testEnvelope("first")); err != nil {
		t.Fatalf("first Emit returned error: %v", err)
	}
	select {
	case <-sink.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}

	var dropped int
	for i := 0; i < 100; i++ {
		result, err := client.Emit(testEnvelope("overflow"))
		if err != nil {
			t.Fatalf("overflow Emit returned error: %v", err)
		}
		if result.Dropped {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected at least one dropped event")
	}
	if stats := client.Stats(); stats.Dropped == 0 {
		t.Fatalf("Dropped stat = 0, want drops: %#v", stats)
	}
}

func TestFlushDrainsAcceptedEvents(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10})
	defer client.Close(context.Background())

	for i := 0; i < 3; i++ {
		if _, err := client.Emit(testEnvelope("queued")); err != nil {
			t.Fatalf("Emit returned error: %v", err)
		}
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	if got := len(sink.envelopes()); got != 3 {
		t.Fatalf("sink envelopes = %d, want 3", got)
	}
	if stats := client.Stats(); stats.Written != 3 {
		t.Fatalf("Written = %d, want 3: %#v", stats.Written, stats)
	}
}

func TestFlushRespectsContextCancellation(t *testing.T) {
	sink := newBlockingSink()
	client := Start(Options{Sink: sink, QueueSize: 1, BatchSize: 10})
	defer func() {
		close(sink.releaseWrite)
		_ = client.Close(context.Background())
	}()

	if _, err := client.Emit(testEnvelope("first")); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := client.Flush(ctx); err == nil {
		t.Fatal("Flush returned nil, want context timeout")
	}
	select {
	case <-sink.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}
}

func TestFlushReturnsSinkErrorAndStats(t *testing.T) {
	sink := newCaptureSink()
	sink.writeErr = errors.New("write failed")
	client := Start(Options{Sink: sink, BatchSize: 10})
	defer client.Close(context.Background())

	if _, err := client.Emit(testEnvelope("one")); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	err := client.Flush(context.Background())
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Flush error = %v, want write failed", err)
	}
	if stats := client.Stats(); stats.WriteErrors == 0 || !strings.Contains(stats.LastError, "write failed") {
		t.Fatalf("stats did not record write error: %#v", stats)
	}
}

func TestCloseFlushesAndIsIdempotent(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10})

	if _, err := client.Emit(testEnvelope("one")); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
	if got := len(sink.envelopes()); got != 1 {
		t.Fatalf("sink envelopes = %d, want 1", got)
	}
	if got := sink.closes(); got != 1 {
		t.Fatalf("sink Close count = %d, want 1", got)
	}
}

func TestCloseCancellationBeforeStopEnqueueCanRetry(t *testing.T) {
	sink := newBlockingSink()
	client := Start(Options{Sink: sink, QueueSize: 1, BatchSize: 1})

	if _, err := client.Emit(testEnvelope("first")); err != nil {
		t.Fatalf("first Emit returned error: %v", err)
	}
	select {
	case <-sink.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("sink write did not start")
	}
	if result, err := client.Emit(testEnvelope("queued")); err != nil {
		t.Fatalf("queued Emit returned error: %v", err)
	} else if !result.Accepted {
		t.Fatalf("queued Emit result = %#v, want accepted", result)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := client.Close(ctx); err == nil {
		t.Fatal("Close returned nil, want context timeout before stop enqueue")
	}

	close(sink.releaseWrite)
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("retry Close returned error: %v", err)
	}
	if got := sink.closes(); got != 1 {
		t.Fatalf("sink Close count = %d, want 1", got)
	}
}

func TestEmitAfterCloseDrops(t *testing.T) {
	client := Start(Options{})
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	result, err := client.Emit(testEnvelope("after-close"))
	if err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if !result.Dropped || result.Accepted {
		t.Fatalf("Emit result = %#v, want dropped", result)
	}
}

func TestPrivacyGateRunsBeforeSinkAndDoesNotMutateCaller(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10, ContentRetention: ContentRetentionFull})
	defer client.Close(context.Background())

	raw := map[string]interface{}{
		"command": "curl -H 'Authorization: Bearer secret-token'",
		"nested":  map[string]interface{}{"token": "token=nested-secret"},
	}
	if _, err := client.Emit(Envelope{
		Origin:  OriginLocal,
		Harness: HarnessInfo{Name: "custom_agent"},
		Raw:     raw,
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	envelopes := sink.envelopes()
	if len(envelopes) != 1 {
		t.Fatalf("sink envelopes = %d, want 1", len(envelopes))
	}
	if strings.Contains(envelopes[0].Raw["command"].(string), "secret-token") {
		t.Fatalf("sink saw unredacted raw: %#v", envelopes[0].Raw)
	}
	if raw["command"] != "curl -H 'Authorization: Bearer secret-token'" {
		t.Fatalf("caller raw map was mutated: %#v", raw)
	}
}

func TestMetadataRetentionOmitsRawPayloadDetails(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, BatchSize: 10, ContentRetention: ContentRetentionMetadata})
	defer client.Close(context.Background())

	if _, err := client.Emit(Envelope{
		Origin:  OriginLocal,
		Harness: HarnessInfo{Name: "custom_agent"},
		Raw:     map[string]interface{}{"prompt": "sensitive", "command": "secret"},
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if err := client.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}

	envelopes := sink.envelopes()
	if len(envelopes) != 1 {
		t.Fatalf("sink envelopes = %d, want 1", len(envelopes))
	}
	if envelopes[0].Raw["field_count"] != 2 {
		t.Fatalf("metadata raw = %#v, want field_count 2", envelopes[0].Raw)
	}
	if _, ok := envelopes[0].Raw["prompt"]; ok {
		t.Fatalf("metadata retention leaked raw prompt: %#v", envelopes[0].Raw)
	}
}

func TestConcurrentEmitFlushAndCloseAreRaceSafe(t *testing.T) {
	sink := newCaptureSink()
	client := Start(Options{Sink: sink, QueueSize: 512, BatchSize: 32})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = client.Emit(testEnvelope("concurrent"))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = client.Flush(context.Background())
		}
	}()
	wg.Wait()

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	stats := client.Stats()
	if stats.Accepted+stats.Dropped != 1000 {
		t.Fatalf("accepted+dropped = %d, want 1000: %#v", stats.Accepted+stats.Dropped, stats)
	}
}

func testEnvelope(value string) Envelope {
	return Envelope{
		Origin:  OriginLocal,
		Harness: HarnessInfo{Name: "test_agent"},
		Raw:     map[string]interface{}{"value": value},
	}
}
