package asymptotetrace

import "context"

type Sink interface {
	WriteBatch(ctx context.Context, envelopes []Envelope) error
	Flush(ctx context.Context) error
	Close() error
}

type NoopSink struct{}

func (NoopSink) WriteBatch(context.Context, []Envelope) error {
	return nil
}

func (NoopSink) Flush(context.Context) error {
	return nil
}

func (NoopSink) Close() error {
	return nil
}
