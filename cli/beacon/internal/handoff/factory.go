package handoff

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/factorysession"
)

// Factory Droid keeps one JSONL transcript per session under a directory named for where it ran,
// opened by a session_start record that carries its title and working directory.

type factorySource struct{ dir string }

func (s *factorySource) Harness() string { return HarnessFactory }

// store reads dir as Factory's sessions directory, as `beacon endpoint factory sync --sessions-dir`
// does.
func (s *factorySource) store() (*factorysession.Store, error) {
	return factorysession.NewStore(s.dir)
}

func (s *factorySource) List() ([]Session, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	if !store.Exists() {
		return nil, nil
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(refs))
	for _, ref := range refs {
		sessions = append(sessions, Session{
			Harness:    HarnessFactory,
			ID:         ref.ID,
			Title:      oneLine(ref.Title),
			Directory:  ref.CWD,
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
		})
	}
	return sessions, nil
}

func (s *factorySource) Events(session Session) ([]schema.Event, error) {
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
		mapped := factorysession.MapSession(ref, records, factorysession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}
