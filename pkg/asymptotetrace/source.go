package asymptotetrace

import "context"

// Record is one source emission. Exactly one of Envelope or Event should be set.
type Record struct {
	Envelope *Envelope
	Event    *Event
}

func NewEnvelopeRecord(envelope Envelope) Record {
	return Record{Envelope: &envelope}
}

func NewEventRecord(event Event) Record {
	return Record{Event: &event}
}

// Validate checks that the record contains exactly one payload.
func (r Record) Validate() error {
	switch {
	case r.Envelope != nil && r.Event != nil:
		return ErrRecordMixed
	case r.Envelope == nil && r.Event == nil:
		return ErrRecordEmpty
	default:
		return nil
	}
}

// Source emits raw or normalized trace records from an agent harness or integration.
type Source interface {
	Run(ctx context.Context, emit func(Record) error) error
}

type staticSource []Record

func (s staticSource) Run(ctx context.Context, emit func(Record) error) error {
	for _, record := range s {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := emit(record); err != nil {
			return err
		}
	}
	return nil
}
