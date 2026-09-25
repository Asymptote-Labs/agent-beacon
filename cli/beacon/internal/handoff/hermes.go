package handoff

import (
	"errors"
	"os"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
)

// Hermes Agent keeps every session in one SQLite database, state.db, so a session's SourcePath is
// that database rather than a file of its own.

type hermesSource struct{ path string }

func (s *hermesSource) Harness() string { return HarnessHermes }

// dbPath reads path as Hermes's state database, as `beacon endpoint hermes sync --db` does.
func (s *hermesSource) dbPath() string {
	if s.path == "" {
		return hermessession.DefaultDBPath()
	}
	return s.path
}

// open opens the state database, or returns nil when there is none.
func (s *hermesSource) open() (*hermessession.Store, error) {
	path := s.dbPath()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return hermessession.OpenStore(path)
}

func (s *hermesSource) List() ([]Session, error) {
	store, err := s.open()
	if err != nil || store == nil {
		return nil, err
	}
	defer store.Close()
	found, err := store.ListSessions()
	if err != nil {
		return nil, err
	}
	details, err := store.SessionDetails()
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(found))
	for _, h := range found {
		detail := details[h.ID]
		title := h.Title
		if title == "" {
			title = detail.FirstPrompt
		}
		updated := max(h.StartedAtMS, h.EndedAtMS, detail.LastMessageAtMS)
		// Only a delegate subagent is a child session here. A compression continuation or a /branch
		// fork also names a parent, but carries the conversation on and is listed as a session.
		parent := ""
		if detail.DelegateFrom != "" {
			parent = h.ParentSessionID
			if parent == "" && detail.DelegateFrom != hermesOrphanedDelegate {
				parent = detail.DelegateFrom
			}
		}
		sessions = append(sessions, Session{
			Harness:    HarnessHermes,
			ID:         h.ID,
			Title:      oneLine(title),
			Directory:  h.CWD,
			Branch:     detail.Branch,
			SourcePath: store.Path(),
			UpdatedAt:  unixMS(updated),
			Subagent:   detail.DelegateFrom != "",
			ParentID:   parent,
		})
	}
	return sessions, nil
}

// hermesOrphanedDelegate is the _delegate_from value Hermes leaves on a subagent whose parent was
// deleted.
const hermesOrphanedDelegate = "__orphaned__"

func (s *hermesSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errSessionGone(session)
	}
	defer store.Close()
	found, err := store.ListSessions()
	if err != nil {
		return nil, err
	}
	for _, h := range found {
		if h.ID != session.ID {
			continue
		}
		messages, err := store.ReadMessages(h.ID, 0)
		if err != nil {
			return nil, err
		}
		// A nil cursor maps the whole session, as a first sync would.
		mapped, _ := hermessession.MapSession(h, messages, nil)
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}
