package asymptotetrace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultBufferSize = 1024
	DefaultTracePath  = "./asymptote-trace.jsonl"
)

var (
	ErrHarnessRequired = errors.New("harness is required")
	ErrTracerClosed    = errors.New("tracer is closed")
)

type Options struct {
	Harness    string
	Origin     Origin
	Session    *SessionInfo
	Run        *RunInfo
	Sink       Sink
	Path       string
	Privacy    *PrivacyPolicy
	BufferSize int
}

type Capture struct {
	Action   string
	Category string
	Severity Severity
	Time     time.Time
	Message  string
	Input    interface{}
	Output   interface{}
	Error    error
	Raw      map[string]interface{}
}

type ObserveResult struct {
	Accepted bool
	Dropped  bool
}

type TracerStats struct {
	Accepted  uint64
	Dropped   uint64
	Errors    uint64
	LastError string
}

type Tracer struct {
	opts     Options
	commands chan tracerCommand
	done     chan struct{}

	accepting atomic.Bool
	closing   atomic.Bool
	closeMu   sync.Mutex

	accepted  atomic.Uint64
	dropped   atomic.Uint64
	errors    atomic.Uint64
	errMu     sync.Mutex
	lastError string
}

type tracerCommand struct {
	kind    tracerCommandKind
	capture Capture
	ack     chan error
}

type tracerCommandKind int

const (
	tracerCommandCapture tracerCommandKind = iota
	tracerCommandFlush
	tracerCommandStop
)

func Start(opts Options) (*Tracer, error) {
	normalized, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}
	tracer := &Tracer{
		opts:     normalized,
		commands: make(chan tracerCommand, normalized.BufferSize),
		done:     make(chan struct{}),
	}
	tracer.accepting.Store(true)
	go tracer.run()
	return tracer, nil
}

func (t *Tracer) Observe(ctx context.Context, capture Capture) (ObserveResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !t.accepting.Load() {
		t.dropped.Add(1)
		return ObserveResult{Dropped: true}, ErrTracerClosed
	}
	select {
	case t.commands <- tracerCommand{kind: tracerCommandCapture, capture: capture}:
		t.accepted.Add(1)
		return ObserveResult{Accepted: true}, nil
	case <-ctx.Done():
		return ObserveResult{}, ctx.Err()
	default:
		t.dropped.Add(1)
		return ObserveResult{Dropped: true}, nil
	}
}

func (t *Tracer) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return t.sendBarrier(ctx, tracerCommandFlush)
}

func (t *Tracer) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.closing.Load() {
		select {
		case <-t.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closing.Load() {
		select {
		case <-t.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ack := make(chan error, 1)
	select {
	case t.commands <- tracerCommand{kind: tracerCommandStop, ack: ack}:
		t.closing.Store(true)
		t.accepting.Store(false)
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-t.done:
		select {
		case err := <-ack:
			return err
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Tracer) Stats() TracerStats {
	t.errMu.Lock()
	lastError := t.lastError
	t.errMu.Unlock()
	return TracerStats{
		Accepted:  t.accepted.Load(),
		Dropped:   t.dropped.Load(),
		Errors:    t.errors.Load(),
		LastError: lastError,
	}
}

func (t *Tracer) sendBarrier(ctx context.Context, kind tracerCommandKind) error {
	ack := make(chan error, 1)
	select {
	case <-t.done:
		return nil
	case t.commands <- tracerCommand{kind: kind, ack: ack}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-t.done:
		select {
		case err := <-ack:
			return err
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Tracer) run() {
	defer close(t.done)
	for command := range t.commands {
		switch command.kind {
		case tracerCommandCapture:
			if err := t.writeCapture(context.Background(), command.capture); err != nil {
				t.recordError(err)
			}
		case tracerCommandFlush:
			command.ack <- t.opts.Sink.Flush(context.Background())
		case tracerCommandStop:
			// Drain captures that raced with stop (enqueued after stop but
			// before accepting was set to false by the caller).
		drain:
			for {
				select {
				case cmd := <-t.commands:
					switch cmd.kind {
					case tracerCommandCapture:
						if err := t.writeCapture(context.Background(), cmd.capture); err != nil {
							t.recordError(err)
						}
					case tracerCommandFlush:
						cmd.ack <- nil
					case tracerCommandStop:
						cmd.ack <- nil
					}
				default:
					break drain
				}
			}
			err := t.opts.Sink.Flush(context.Background())
			if closeErr := t.opts.Sink.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			command.ack <- err
			return
		}
	}
}

func (t *Tracer) writeCapture(ctx context.Context, capture Capture) error {
	event := t.eventFromCapture(capture)
	if err := event.Validate(); err != nil {
		return err
	}
	events := []Event{event}
	var err error
	for _, processor := range t.opts.processors() {
		events, err = processor.Process(ctx, events)
		if err != nil {
			return err
		}
	}
	if len(events) == 0 {
		return nil
	}
	return t.opts.Sink.WriteBatch(ctx, events)
}

func (t *Tracer) eventFromCapture(capture Capture) Event {
	action := capture.Action
	if action == "" {
		action = UnclassifiedTraceAction
	}
	category := capture.Category
	if category == "" {
		category = "trace"
	}
	message := capture.Message
	if capture.Error != nil && message == "" {
		message = capture.Error.Error()
	}
	event := NewEvent(NewEventOptions{
		Action:   action,
		Category: category,
		Severity: capture.Severity,
		Harness:  HarnessInfo{Name: t.opts.Harness},
		Message:  message,
		Origin:   t.opts.Origin,
		Run:      cloneRun(t.opts.Run),
	})
	if !capture.Time.IsZero() {
		event.Timestamp = capture.Time.UTC().Format(time.RFC3339)
	}
	event.Session = cloneSession(t.opts.Session)
	event.Raw = captureRaw(capture)
	return event
}

func captureRaw(capture Capture) map[string]interface{} {
	raw := copyMap(capture.Raw)
	if raw == nil {
		raw = map[string]interface{}{}
	}
	if capture.Input != nil {
		raw["input"] = capture.Input
	}
	if capture.Output != nil {
		raw["output"] = capture.Output
	}
	if capture.Error != nil {
		raw["error"] = capture.Error.Error()
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func (t *Tracer) recordError(err error) {
	if err == nil {
		return
	}
	t.errors.Add(1)
	t.errMu.Lock()
	t.lastError = err.Error()
	t.errMu.Unlock()
}

func (opts Options) withDefaults() (Options, error) {
	if opts.Harness == "" {
		return Options{}, ErrHarnessRequired
	}
	if opts.Origin == "" {
		opts.Origin = OriginLocal
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultBufferSize
	}
	if opts.Sink == nil {
		path := opts.Path
		if path == "" {
			path = DefaultTracePath
		}
		opts.Sink = NewJSONLSink(path)
	}
	return opts, nil
}

func (opts Options) processors() []Processor {
	if opts.Privacy == nil {
		return nil
	}
	return []Processor{NewPrivacyProcessor(*opts.Privacy)}
}
