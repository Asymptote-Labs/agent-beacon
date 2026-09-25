package handoff

import (
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/fxsession"
)

// fx keeps one directory per session: an append-only events.jsonl and a session.json manifest that
// names the workspace the session is bound to now. fx records no title and no git branch.

type fxSource struct{ dir string }

func (s *fxSource) Harness() string { return HarnessFx }

// store reads dir as fx's sessions directory, as `beacon endpoint fx sync --sessions-dir` does.
func (s *fxSource) store() (*fxsession.Store, error) { return fxsession.NewStore(s.dir) }

func (s *fxSource) List() ([]Session, error) {
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
		head := readFxHead(ref.EventsLog)
		session := Session{
			Harness:    HarnessFx,
			ID:         ref.ID,
			Title:      oneLine(head.FirstPrompt),
			Directory:  head.Workspace,
			SourcePath: ref.EventsLog,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
		}
		// The manifest is fx's own projection of the session. Its workspace follows a session that
		// was rebound to another directory, which the log's first event does not.
		if m := ref.Manifest; m != nil {
			if dir := firstNonEmpty(m.WorkspaceRoot, m.OriginWorkspaceRoot); dir != "" {
				session.Directory = dir
			}
			if m.UpdatedAtMS > 0 {
				session.UpdatedAt = unixMS(m.UpdatedAtMS)
			}
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (s *fxSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.ID != session.ID || ref.EventsLog != session.SourcePath {
			continue
		}
		entries, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := fxsession.MapSession(ref, entries, fxsession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

type fxHead struct {
	Workspace   string
	FirstPrompt string
}

// readFxHead reads where an fx session started and its first prompt from the start of its events
// log. fx commits a whole turn, prompt included, as one line, so the first prompt is near the top.
func readFxHead(path string) fxHead {
	var head fxHead
	scanHeadLines(path, fxsession.MaxFrameBytes, func(line []byte) bool {
		event, err := fxsession.DecodeEnvelope(line)
		if err != nil {
			return false
		}
		if started := event.SessionStarted; started != nil && head.Workspace == "" {
			head.Workspace = firstNonEmpty(started.WorkspaceRoot.String(), started.OriginWorkspaceRoot.String())
		}
		if turn := event.TurnCommitted; turn != nil && turn.Turn.User != nil {
			head.FirstPrompt = turn.Turn.User.Text.String()
		}
		return head.FirstPrompt != ""
	})
	return head
}
