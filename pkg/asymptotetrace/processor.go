package asymptotetrace

import "context"

// Processor transforms canonical events before they reach a sink.
type Processor interface {
	Process(ctx context.Context, events []Event) ([]Event, error)
}

// PrivacyPolicy controls retention and size limits before events reach sinks.
type PrivacyPolicy struct {
	Retention     string
	MaxEventBytes int
}

// PrivacyProcessor applies redaction, truncation, and retention to events.
type PrivacyProcessor struct {
	policy PrivacyPolicy
}

// NewPrivacyProcessor creates a privacy processor with fail-closed defaults.
func NewPrivacyProcessor(policy PrivacyPolicy) PrivacyProcessor {
	return PrivacyProcessor{policy: policy.withDefaults()}
}

func (p PrivacyProcessor) Process(ctx context.Context, events []Event) ([]Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	out := make([]Event, 0, len(events))
	for _, event := range events {
		out = append(out, prepareEventForSink(event, p.policy.Retention, p.policy.MaxEventBytes))
	}
	return out, nil
}

func (policy PrivacyPolicy) withDefaults() PrivacyPolicy {
	if policy.Retention == "" {
		policy.Retention = ContentRetentionFull
	}
	if !validContentRetention(policy.Retention) {
		policy.Retention = ContentRetentionMetadata
	}
	if policy.MaxEventBytes <= 0 {
		policy.MaxEventBytes = DefaultMaxEventBytes
	}
	return policy
}
