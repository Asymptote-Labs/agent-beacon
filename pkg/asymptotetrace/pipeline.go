package asymptotetrace

import (
	"context"
	"errors"
)

const DefaultMaxEventBytes = 64 * 1024

var (
	// ErrPipelineMissingSource is returned when a pipeline has no source.
	ErrPipelineMissingSource = errors.New("trace pipeline requires a source")
	// ErrRecordEmpty is returned when a source emits an empty record.
	ErrRecordEmpty = errors.New("trace record requires envelope or event")
	// ErrRecordMixed is returned when a source emits both envelope and event.
	ErrRecordMixed = errors.New("trace record accepts either envelope or event, not both")
)

// PipelineOptions configures a one-shot trace export pipeline.
type PipelineOptions struct {
	Source     Source
	Normalizer Normalizer
	Processors []Processor
	Sink       Sink
	Privacy    *PrivacyPolicy
	// KeepSinkOpen leaves sink lifecycle ownership with the caller.
	KeepSinkOpen bool
}

// Pipeline exports trace data from a source through normalization, privacy, and a sink.
type Pipeline struct {
	opts PipelineOptions
}

func NewPipeline(opts PipelineOptions) *Pipeline {
	return &Pipeline{opts: opts.withDefaults()}
}

// Validate checks whether options can run after defaults are applied.
func (opts PipelineOptions) Validate() error {
	if opts.Source == nil {
		return ErrPipelineMissingSource
	}
	return nil
}

// Run executes a one-shot pipeline and owns the sink lifecycle: it flushes and
// closes the sink before returning unless KeepSinkOpen is true.
func (p *Pipeline) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.opts.Validate(); err != nil {
		return err
	}

	runErr := p.opts.Source.Run(ctx, func(record Record) error {
		events, err := p.eventsFromRecord(ctx, record)
		if err != nil {
			return err
		}
		return p.writeEvents(ctx, events)
	})
	if runErr != nil {
		p.closeSink()
		return runErr
	}
	if err := p.opts.Sink.Flush(ctx); err != nil {
		p.closeSink()
		return err
	}
	return p.closeSink()
}

func (p *Pipeline) writeEvents(ctx context.Context, events []Event) error {
	processed := events
	var err error
	for _, processor := range p.opts.Processors {
		processed, err = processor.Process(ctx, processed)
		if err != nil {
			return err
		}
	}
	if len(processed) == 0 {
		return nil
	}
	return p.opts.Sink.WriteBatch(ctx, processed)
}

func (p *Pipeline) eventsFromRecord(ctx context.Context, record Record) ([]Event, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.Envelope != nil {
		return p.opts.Normalizer.Normalize(ctx, record.Envelope.copy())
	}
	return []Event{copyEvent(*record.Event)}, nil
}

func (p *Pipeline) closeSink() error {
	if p.opts.KeepSinkOpen {
		return nil
	}
	return p.opts.Sink.Close()
}

func (opts PipelineOptions) withDefaults() PipelineOptions {
	if opts.Normalizer == nil {
		opts.Normalizer = UnclassifiedNormalizer{}
	}
	if opts.Sink == nil {
		opts.Sink = NoopSink{}
	}
	if opts.Privacy != nil && !hasPrivacyProcessor(opts.Processors) {
		opts.Processors = append([]Processor{NewPrivacyProcessor(*opts.Privacy)}, opts.Processors...)
	}
	return opts
}

func hasPrivacyProcessor(processors []Processor) bool {
	for _, processor := range processors {
		if _, ok := processor.(PrivacyProcessor); ok {
			return true
		}
		if _, ok := processor.(*PrivacyProcessor); ok {
			return true
		}
	}
	return false
}

func prepareEventForSink(event Event, retention string, maxBytes int) Event {
	out := copyEvent(event)
	if out.Content == nil {
		out.Content = &ContentInfo{}
	}
	out.Content.Retention = retention
	out.Content.Included = retention != ContentRetentionMetadata
	out.Content.Redacted = retention == ContentRetentionRedacted
	if out.Raw != nil {
		out.Raw = SanitizeMap(out.Raw, PrivacyOptions{RedactSecrets: true, StringLimit: DefaultRawStringLimit})
		out.Raw = RetentionAwareRaw(out.Raw, retention)
	}
	return SanitizeEvent(out, maxBytes)
}

func copyEvent(event Event) Event {
	out := event
	out.Run = cloneRun(event.Run)
	out.Session = cloneSession(event.Session)
	if event.Tool != nil {
		tool := *event.Tool
		out.Tool = &tool
	}
	if event.File != nil {
		file := *event.File
		out.File = &file
	}
	if event.Command != nil {
		command := *event.Command
		out.Command = &command
	}
	if event.MCP != nil {
		mcp := *event.MCP
		out.MCP = &mcp
	}
	if event.Approval != nil {
		approval := *event.Approval
		out.Approval = &approval
	}
	if event.Policy != nil {
		policy := *event.Policy
		out.Policy = &policy
	}
	if event.Prompt != nil {
		prompt := *event.Prompt
		out.Prompt = &prompt
	}
	if event.Content != nil {
		content := *event.Content
		out.Content = &content
	}
	if event.Destination != nil {
		destination := *event.Destination
		out.Destination = &destination
	}
	if event.Health != nil {
		health := *event.Health
		out.Health = &health
	}
	if event.Raw != nil {
		out.Raw = copyMap(event.Raw)
	}
	return out
}

func validContentRetention(retention string) bool {
	switch retention {
	case ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull:
		return true
	default:
		return false
	}
}
