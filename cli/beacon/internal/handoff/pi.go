package handoff

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pisession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/primesession"
)

// Pi and Prime Agent keep one JSONL transcript per session, opened by a header line that names the
// session, where it ran and its git branch. Prime Agent is a Pi fork with the same transcript
// format and a second root for subagent transcripts.

type piSource struct{ dir string }

func (s *piSource) Harness() string { return HarnessPi }

// store reads dir as Pi's sessions directory, as `beacon endpoint pi sync --sessions-dir` does.
func (s *piSource) store() (*pisession.Store, error) { return pisession.NewStore(s.dir) }

func (s *piSource) List() ([]Session, error) {
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
			Harness:    HarnessPi,
			ID:         ref.ID,
			Title:      oneLine(readPiFirstPrompt(ref.Path)),
			Directory:  ref.Workspace,
			Branch:     piHeaderBranch(ref.Header),
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeMS),
		})
	}
	return sessions, nil
}

func (s *piSource) Events(session Session) ([]schema.Event, error) {
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
		entries, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := pisession.MapSession(ref, entries, pisession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

type primeSource struct{ dir string }

func (s *primeSource) Harness() string { return HarnessPrime }

// store reads dir as Prime Agent's agent directory, which holds both of its session roots. Empty
// means the default, honouring PRIME_AGENT_CODING_AGENT_DIR as `beacon endpoint prime sync` does.
func (s *primeSource) store() (*primesession.Store, error) {
	if s.dir == "" {
		return primesession.NewStore("", "")
	}
	return primesession.NewStore(filepath.Join(s.dir, "sessions"), filepath.Join(s.dir, "session-artifacts"))
}

func (s *primeSource) List() ([]Session, error) {
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
		parent := piHeaderString(ref.Header, "parentSession")
		sessions = append(sessions, Session{
			Harness:    HarnessPrime,
			ID:         ref.ID,
			Title:      oneLine(readPiFirstPrompt(ref.Path)),
			Directory:  piHeaderString(ref.Header, "cwd"),
			Branch:     piHeaderBranch(ref.Header),
			SourcePath: ref.Path,
			Store:      ref.Kind,
			UpdatedAt:  unixMS(ref.ModTimeMS),
			// Subagent transcripts live under session-artifacts and name the session that spawned them.
			Subagent: ref.Kind == primeArtifactKind || parent != "",
			ParentID: parent,
		})
	}
	return sessions, nil
}

const primeArtifactKind = "artifact"

func (s *primeSource) Events(session Session) ([]schema.Event, error) {
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
		entries, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := primesession.MapSession(ref, entries, primesession.MapOptions{
			SubagentOf: piHeaderString(ref.Header, "parentSession"),
		})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

func piHeaderString(header map[string]interface{}, key string) string {
	value, _ := header[key].(string)
	return strings.TrimSpace(value)
}

func piHeaderBranch(header map[string]interface{}) string {
	git, _ := header["git"].(map[string]interface{})
	branch, _ := git["branch"].(string)
	return strings.TrimSpace(branch)
}

// readPiFirstPrompt returns the first thing a person typed in a Pi-format transcript.
func readPiFirstPrompt(path string) string {
	var prompt string
	scanHead(path, func(line []byte) bool {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "message" || entry.Message.Role != "user" {
			return false
		}
		prompt = piContentText(entry.Message.Content)
		return prompt != ""
	})
	return prompt
}

// piContentText is a message's first text: the content itself when it is a string, else its first
// text block. Pi writes blocks with and without a "type".
func piContentText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		if (block.Type == "" || block.Type == "text") && strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}
