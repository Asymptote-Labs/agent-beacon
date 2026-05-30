package asymptotetrace

import "context"

const UnclassifiedTraceAction = "trace.unclassified"

// Normalizer maps raw envelopes into canonical Beacon events.
type Normalizer interface {
	Normalize(ctx context.Context, envelope Envelope) ([]Event, error)
}

// UnclassifiedNormalizer preserves raw envelope metadata as an unclassified trace event.
type UnclassifiedNormalizer struct{}

func (UnclassifiedNormalizer) Normalize(ctx context.Context, envelope Envelope) ([]Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	envelope = envelope.withDefaults()
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	event := NewEvent(NewEventOptions{
		Action:   UnclassifiedTraceAction,
		Category: "trace",
		Severity: SeverityInfo,
		Harness:  envelope.Harness,
		Origin:   envelope.Origin,
		Run:      cloneRun(envelope.Run),
		Message:  "Trace received but not classified",
	})
	event.Session = cloneSession(envelope.Session)
	if envelope.Raw != nil {
		event.Raw = copyMap(envelope.Raw)
	}
	return []Event{event}, nil
}

func cloneSession(session *SessionInfo) *SessionInfo {
	if session == nil {
		return nil
	}
	out := *session
	return &out
}

func cloneRun(run *RunInfo) *RunInfo {
	if run == nil {
		return nil
	}
	out := *run
	return &out
}
