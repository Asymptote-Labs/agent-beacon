package asymptotetrace

import "context"

// Sink receives canonical Beacon events from a trace pipeline.
type Sink interface {
	WriteBatch(ctx context.Context, events []Event) error
	Flush(ctx context.Context) error
	Close() error
}

// NoopSink discards all events.
type NoopSink struct{}

func (NoopSink) WriteBatch(context.Context, []Event) error {
	return nil
}

func (NoopSink) Flush(context.Context) error {
	return nil
}

func (NoopSink) Close() error {
	return nil
}
