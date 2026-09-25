package handoff

import (
	"fmt"
	"sort"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
)

// EventSource is a Source that can also read one session back as endpoint events, through the
// same mapper its `beacon endpoint <runtime> sync` backfill uses.
type EventSource interface {
	Source
	Events(Session) ([]schema.Event, error)
}

// Events reads a session's events from the source for its runtime.
func Events(sources []Source, session Session) ([]schema.Event, error) {
	for _, source := range sources {
		if source.Harness() != session.Harness {
			continue
		}
		reader, ok := source.(EventSource)
		if !ok {
			return nil, fmt.Errorf("%s sessions cannot be read back", session.Harness)
		}
		return reader.Events(session)
	}
	return nil, fmt.Errorf("no session store for %s", session.Harness)
}

func errSessionGone(session Session) error {
	return fmt.Errorf("%w: %s session %s is no longer in its store", ErrNotFound, session.Harness, session.ID)
}

func (s *claudeSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.ID != session.ID || ref.Path != session.SourcePath {
			continue
		}
		records, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		return claudeEvents(claudesession.MapSession(ref, records, claudesession.MapOptions{})), nil
	}
	return nil, errSessionGone(session)
}

func claudeEvents(mapped []claudesession.MappedEvent) []schema.Event {
	events := make([]schema.Event, 0, len(mapped))
	for _, m := range mapped {
		events = append(events, m.Event)
	}
	return events
}

// Events reads every rollout file of the thread, oldest first, so a thread resumed into a new file
// keeps its earlier turns.
func (s *codexSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	var thread []codexsession.SessionRef
	for _, ref := range refs {
		if ref.ID == session.ID {
			thread = append(thread, ref)
		}
	}
	if len(thread) == 0 {
		return nil, errSessionGone(session)
	}
	sort.SliceStable(thread, func(i, j int) bool { return thread[i].ModTimeUnixMS < thread[j].ModTimeUnixMS })
	var events []schema.Event
	for i, ref := range thread {
		records, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		// A later rollout file repeats the thread's session_meta; only the first starts the session.
		for _, m := range codexsession.MapSession(ref, records, codexsession.MapOptions{SkipSessionStarted: i > 0}) {
			events = append(events, m.Event)
		}
	}
	return events, nil
}

func (s *openCodeSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, listErr := store.List()
	for _, ref := range refs {
		if ref.ID != session.ID {
			continue
		}
		records, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := opencodesession.MapTrace(ref, records, opencodesession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	if listErr != nil {
		return nil, listErr
	}
	return nil, errSessionGone(session)
}

func (s *clineSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.ID != session.ID || ref.Kind != session.Store {
			continue
		}
		records, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := clinesession.MapTrace(ref, records, clinesession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}
