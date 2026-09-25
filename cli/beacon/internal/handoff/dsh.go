package handoff

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/dshsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// DeepSeek Harness keeps one directory per session under $DSH_HOME/sessions, holding a record
// file whose first record names the session, where it ran, and the session it came from.

type dshSource struct{ dir string }

func (s *dshSource) Harness() string { return HarnessDSH }

// store reads dir as DSH_HOME, as `beacon endpoint dsh sync --dsh-home` does. Empty means the
// default, honouring $DSH_HOME.
func (s *dshSource) store() (*dshsession.Store, error) { return dshsession.NewStore(s.dir) }

func (s *dshSource) List() ([]Session, error) {
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
		session := Session{
			Harness:    HarnessDSH,
			ID:         ref.ID,
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
		}
		if meta := ref.Meta; meta != nil {
			session.Title = oneLine(firstNonEmpty(meta.Title, meta.FirstPrompt))
			session.Directory = meta.CWD
			// A fork names its parent session too, but is a session of its own; only a session DSH
			// spawned for delegated work is a subagent.
			if meta.Origin == "subagent" {
				session.Subagent, session.ParentID = true, meta.ParentSessionID
			}
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (s *dshSource) Events(session Session) ([]schema.Event, error) {
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
		mapped := dshsession.MapSession(ref, records, dshsession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}
