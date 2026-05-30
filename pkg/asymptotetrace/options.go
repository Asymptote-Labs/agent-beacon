package asymptotetrace

import "time"

const (
	DefaultQueueSize     = 1024
	DefaultBatchSize     = 64
	DefaultFlushInterval = 5 * time.Second
)

type Options struct {
	Sink             Sink
	QueueSize        int
	BatchSize        int
	FlushInterval    time.Duration
	ContentRetention string
}

func (opts Options) withDefaults() Options {
	if opts.Sink == nil {
		opts.Sink = NoopSink{}
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.ContentRetention == "" {
		opts.ContentRetention = ContentRetentionFull
	}
	if !validContentRetention(opts.ContentRetention) {
		// Retention controls are privacy-sensitive; invalid values fail closed.
		opts.ContentRetention = ContentRetentionMetadata
	}
	return opts
}

func validContentRetention(retention string) bool {
	switch retention {
	case ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull:
		return true
	default:
		return false
	}
}
