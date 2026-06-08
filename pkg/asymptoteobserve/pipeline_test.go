package asymptoteobserve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type captureEventSink struct {
	mu         sync.Mutex
	events     []Event
	writeErr   error
	flushErr   error
	closeErr   error
	flushCount int
	closeCount int
}

func (s *captureEventSink) WriteBatch(ctx context.Context, events []Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	copied := make([]Event, len(events))
	for i, event := range events {
		copied[i] = copyEvent(event)
	}
	s.mu.Lock()
	s.events = append(s.events, copied...)
	s.mu.Unlock()
	return nil
}

func (s *captureEventSink) Flush(context.Context) error {
	s.mu.Lock()
	s.flushCount++
	s.mu.Unlock()
	return s.flushErr
}

func (s *captureEventSink) Close() error {
	s.mu.Lock()
	s.closeCount++
	s.mu.Unlock()
	return s.closeErr
}

func (s *captureEventSink) snapshot() ([]Event, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]Event, len(s.events))
	copy(events, s.events)
	return events, s.flushCount, s.closeCount
}

type errorRawTraceSource struct {
	err error
}

func (s errorRawTraceSource) Run(context.Context, func(Record) error) error {
	return s.err
}

type errorNormalizer struct {
	err error
}

func (n errorNormalizer) Normalize(context.Context, Envelope) ([]Event, error) {
	return nil, n.err
}

type captureProcessor struct {
	seen []Event
}

func (p *captureProcessor) Process(ctx context.Context, events []Event) ([]Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	p.seen = append(p.seen, events...)
	return events, nil
}

func TestPipelineEnvelopeRecordNormalizesAndWritesEvents(t *testing.T) {
	sink := &captureEventSink{}
	pipeline := NewPipeline(PipelineOptions{
		Source: staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:   sink,
	})

	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	events, flushes, closes := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Vendor != Vendor || event.Product != Product || event.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected schema identity: %#v", event)
	}
	if event.Event.Action != UnclassifiedTraceAction || event.Event.Category != "trace" {
		t.Fatalf("unexpected event info: %#v", event.Event)
	}
	if event.Harness.Name != "custom_agent" || event.Origin != OriginLocal {
		t.Fatalf("harness/origin not preserved: %#v", event)
	}
	if event.Session == nil || event.Session.ID != "session-1" {
		t.Fatalf("session not preserved: %#v", event.Session)
	}
	if event.Run == nil || event.Run.Provider != "github_actions" {
		t.Fatalf("run not preserved: %#v", event.Run)
	}
	if flushes != 1 || closes != 1 {
		t.Fatalf("flushes/closes = %d/%d, want 1/1", flushes, closes)
	}
}

func TestPipelineEventRecordSkipsNormalizer(t *testing.T) {
	sink := &captureEventSink{}
	event := NewEvent(NewEventOptions{
		Action:   "tool.invoked",
		Category: "tool",
		Harness:  HarnessInfo{Name: "cursor"},
	})
	pipeline := NewPipeline(PipelineOptions{
		Source:     staticSource{NewEventRecord(event)},
		Normalizer: errorNormalizer{err: errors.New("normalizer should not be called")},
		Sink:       sink,
	})

	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	events, _, _ := sink.snapshot()
	if len(events) != 1 || events[0].Event.Action != "tool.invoked" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestPipelinePrivacyBeforeSinkAndNoEnvelopeMutation(t *testing.T) {
	sink := &captureEventSink{}
	envelope := testEnvelope()
	envelope.Raw["command"] = "curl -H 'Authorization: Bearer secret-token'"
	envelope.Raw["nested"] = map[string]interface{}{"token": "token=nested-secret"}

	pipeline := NewPipeline(PipelineOptions{
		Source:  staticSource{NewEnvelopeRecord(envelope)},
		Sink:    sink,
		Privacy: &PrivacyPolicy{MaxEventBytes: DefaultMaxEventBytes},
	})
	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	events, _, _ := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	rawText := stringify(events[0].Raw)
	if strings.Contains(rawText, "secret-token") || strings.Contains(rawText, "nested-secret") {
		t.Fatalf("sink saw unredacted raw: %s", rawText)
	}
	if envelope.Raw["command"] != "curl -H 'Authorization: Bearer secret-token'" {
		t.Fatalf("source envelope was mutated: %#v", envelope.Raw)
	}
}

func TestPipelineWithoutPrivacyPassesEventsThrough(t *testing.T) {
	sink := &captureEventSink{}
	envelope := testEnvelope()
	envelope.Raw["token"] = "token=visible-without-privacy"

	pipeline := NewPipeline(PipelineOptions{
		Source: staticSource{NewEnvelopeRecord(envelope)},
		Sink:   sink,
	})
	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	events, _, _ := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if rawText := stringify(events[0].Raw); !strings.Contains(rawText, "visible-without-privacy") {
		t.Fatalf("event was unexpectedly sanitized without privacy: %s", rawText)
	}
}

func TestPipelinePrivacyRunsBeforeAdditionalProcessors(t *testing.T) {
	sink := &captureEventSink{}
	processor := &captureProcessor{}
	envelope := testEnvelope()
	envelope.Raw["token"] = "token=processor-secret"

	pipeline := NewPipeline(PipelineOptions{
		Source:     staticSource{NewEnvelopeRecord(envelope)},
		Processors: []Processor{processor},
		Sink:       sink,
		Privacy:    &PrivacyPolicy{},
	})
	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(processor.seen) != 1 {
		t.Fatalf("processor saw %d events, want 1", len(processor.seen))
	}
	if rawText := stringify(processor.seen[0].Raw); strings.Contains(rawText, "processor-secret") {
		t.Fatalf("processor saw unredacted raw: %s", rawText)
	}
}

func TestPipelinePrivacyKeepsRawTelemetry(t *testing.T) {
	sink := &captureEventSink{}
	pipeline := NewPipeline(PipelineOptions{
		Source:  staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:    sink,
		Privacy: &PrivacyPolicy{},
	})

	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	events, _, _ := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Raw["prompt"] != "summarize" {
		t.Fatalf("privacy unexpectedly removed raw: %#v", events[0].Raw)
	}
}

func TestPipelineSourceErrorPropagates(t *testing.T) {
	want := errors.New("source failed")
	pipeline := NewPipeline(PipelineOptions{
		Source: errorRawTraceSource{err: want},
		Sink:   &captureEventSink{},
	})

	if err := pipeline.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestPipelineNormalizerErrorPropagates(t *testing.T) {
	want := errors.New("normalize failed")
	pipeline := NewPipeline(PipelineOptions{
		Source:     staticSource{NewEnvelopeRecord(testEnvelope())},
		Normalizer: errorNormalizer{err: want},
		Sink:       &captureEventSink{},
	})

	if err := pipeline.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestPipelineSinkErrorPropagates(t *testing.T) {
	want := errors.New("sink failed")
	pipeline := NewPipeline(PipelineOptions{
		Source: staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:   &captureEventSink{writeErr: want},
	})

	if err := pipeline.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestPipelineFlushErrorPropagates(t *testing.T) {
	want := errors.New("flush failed")
	pipeline := NewPipeline(PipelineOptions{
		Source: staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:   &captureEventSink{flushErr: want},
	})

	if err := pipeline.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestPipelineCloseErrorPropagates(t *testing.T) {
	want := errors.New("close failed")
	pipeline := NewPipeline(PipelineOptions{
		Source: staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:   &captureEventSink{closeErr: want},
	})

	if err := pipeline.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestPipelineCanLeaveSinkOpen(t *testing.T) {
	sink := &captureEventSink{}
	pipeline := NewPipeline(PipelineOptions{
		Source:       staticSource{NewEnvelopeRecord(testEnvelope())},
		Sink:         sink,
		KeepSinkOpen: true,
	})

	if err := pipeline.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	_, flushes, closes := sink.snapshot()
	if flushes != 1 || closes != 0 {
		t.Fatalf("flushes/closes = %d/%d, want 1/0", flushes, closes)
	}
}

func TestPipelineRejectsMissingSource(t *testing.T) {
	if err := NewPipeline(PipelineOptions{Sink: &captureEventSink{}}).Run(context.Background()); !errors.Is(err, ErrPipelineMissingSource) {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestPipelineRejectsEmptyOrMixedRecords(t *testing.T) {
	if err := NewPipeline(PipelineOptions{
		Source: staticSource{{}},
		Sink:   &captureEventSink{},
	}).Run(context.Background()); !errors.Is(err, ErrRecordEmpty) {
		t.Fatalf("empty record error = %v", err)
	}

	envelope := testEnvelope()
	event := NewEvent(NewEventOptions{Action: "tool.invoked", Harness: HarnessInfo{Name: "cursor"}})
	err := NewPipeline(PipelineOptions{
		Source: staticSource{{Envelope: &envelope, Event: &event}},
		Sink:   &captureEventSink{},
	}).Run(context.Background())
	if !errors.Is(err, ErrRecordMixed) {
		t.Fatalf("mixed record error = %v", err)
	}
}

func testEnvelope() Envelope {
	envelope := NewEnvelope(OriginLocal, HarnessInfo{Name: "custom_agent"}, map[string]interface{}{
		"prompt": "summarize",
		"tool":   "bash",
	})
	envelope.Session = &SessionInfo{ID: "session-1", WorkingDirectory: "/repo"}
	envelope.Run = &RunInfo{Provider: "github_actions", RunID: "123", Ephemeral: true}
	return envelope
}

func stringify(value interface{}) string {
	return strings.Join(strings.Fields(fmt.Sprint(value)), " ")
}
