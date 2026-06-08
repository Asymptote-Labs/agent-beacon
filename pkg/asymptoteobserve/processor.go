package asymptoteobserve

import "context"

// Processor transforms canonical events before they reach a sink.
type Processor interface {
	Process(ctx context.Context, events []Event) ([]Event, error)
}

// PrivacyPolicy controls size limits before events reach sinks.
type PrivacyPolicy struct {
	MaxEventBytes int
}

// PrivacyProcessor applies redaction and truncation to events.
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
		out = append(out, prepareEventForSink(event, p.policy.MaxEventBytes))
	}
	return out, nil
}

func (policy PrivacyPolicy) withDefaults() PrivacyPolicy {
	if policy.MaxEventBytes <= 0 {
		policy.MaxEventBytes = DefaultMaxEventBytes
	}
	return policy
}
