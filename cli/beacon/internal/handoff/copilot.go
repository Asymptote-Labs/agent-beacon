package handoff

import (
	"encoding/json"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/copilotsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// GitHub Copilot CLI keeps each session in its own directory under session-state: an events.jsonl
// log, and a workspace.yaml naming the session, where it ran and its git branch.

type copilotSource struct{ dir string }

func (s *copilotSource) Harness() string { return HarnessCopilot }

// store reads dir as Copilot's own directory, as `beacon endpoint copilot sync --copilot-dir` does.
func (s *copilotSource) store() (*copilotsession.Store, error) { return copilotsession.NewStore(s.dir) }

func (s *copilotSource) List() ([]Session, error) {
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
		var meta copilotsession.WorkspaceMeta
		if ref.Meta != nil {
			meta = *ref.Meta
		}
		title := meta.Name
		directory := firstNonEmpty(meta.CWD, meta.GitRoot)
		branch := meta.Branch
		if strings.TrimSpace(title) == "" || strings.TrimSpace(directory) == "" || strings.TrimSpace(branch) == "" {
			// workspace.yaml is written alongside the log and can be missing or not yet named; the
			// log's head records the first prompt and the session's starting context.
			head := readCopilotHead(ref.Path)
			title = firstNonEmpty(title, head.FirstPrompt)
			directory = firstNonEmpty(directory, head.CWD)
			branch = firstNonEmpty(branch, head.Branch)
		}
		sessions = append(sessions, Session{
			Harness:    HarnessCopilot,
			ID:         ref.ID,
			Title:      oneLine(title),
			Directory:  strings.TrimSpace(directory),
			Branch:     strings.TrimSpace(branch),
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
		})
	}
	return sessions, nil
}

func (s *copilotSource) Events(session Session) ([]schema.Event, error) {
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
		mapped := copilotsession.MapSession(ref, records, copilotsession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

type copilotHead struct {
	CWD         string
	Branch      string
	FirstPrompt string
}

// readCopilotHead reads the session's starting context and first prompt from the start of its log.
func readCopilotHead(path string) copilotHead {
	var head copilotHead
	scanHead(path, func(line []byte) bool {
		var record struct {
			Type string `json:"type"`
			Data struct {
				Content string `json:"content"`
				Context struct {
					CWD    string `json:"cwd"`
					Branch string `json:"branch"`
				} `json:"context"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &record) != nil {
			return false
		}
		switch record.Type {
		case "session.start":
			head.CWD = firstNonEmpty(head.CWD, record.Data.Context.CWD)
			head.Branch = firstNonEmpty(head.Branch, record.Data.Context.Branch)
		case "user.message":
			head.FirstPrompt = firstNonEmpty(head.FirstPrompt, record.Data.Content)
		}
		return head.FirstPrompt != ""
	})
	return head
}
