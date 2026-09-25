package handoff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/groksession"
)

// Grok Build keeps one directory per session, named by the session's id, under a directory named
// for the working directory it ran in. summary.json indexes the session.

type grokSource struct{ dir string }

func (s *grokSource) Harness() string { return HarnessGrok }

// store reads dir as Grok's sessions directory, as `beacon endpoint grok sync --sessions-dir` does.
func (s *grokSource) store() (*groksession.Store, error) { return groksession.NewStore(s.dir) }

func (s *grokSource) List() ([]Session, error) {
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
		var summary groksession.Summary
		if ref.Summary != nil {
			summary = *ref.Summary
		} else {
			summary = readGrokSummary(ref.SourcePath)
		}
		title := oneLine(firstNonEmpty(summary.GeneratedTitle, summary.SessionSummary))
		if title == "" {
			title = oneLine(readGrokFirstPrompt(filepath.Join(ref.SourcePath, "chat_history.jsonl")))
		}
		sessions = append(sessions, Session{
			Harness:    HarnessGrok,
			ID:         ref.ID,
			Title:      title,
			Directory:  grokWorkingDirectory(ref),
			Branch:     strings.TrimSpace(summary.HeadBranch),
			SourcePath: ref.SourcePath,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
			// Grok reserves the subagent namespace of session kinds for the child sessions it
			// spawns, which live in the same sessions tree as the sessions a person started.
			Subagent: strings.HasPrefix(strings.ToLower(strings.TrimSpace(summary.SessionKind)), "subagent"),
		})
	}
	return sessions, nil
}

func (s *grokSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.ID != session.ID || ref.SourcePath != session.SourcePath {
			continue
		}
		data, _, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := groksession.MapSession(data, groksession.MapOptions{})
		events := make([]schema.Event, 0, len(mapped))
		for _, m := range mapped {
			events = append(events, m.Event)
		}
		return events, nil
	}
	return nil, errSessionGone(session)
}

// grokWorkingDirectory is where the session ran. The store names its group directory by the
// URL-encoded working directory, but a path too long for one directory name becomes a slug, so the
// session's prompt_context.json, which records the directory itself, is preferred, as the sync's
// own read does.
func grokWorkingDirectory(ref groksession.SessionRef) string {
	data, err := os.ReadFile(filepath.Join(ref.SourcePath, "prompt_context.json"))
	if err == nil {
		var prompt groksession.PromptContext
		if json.Unmarshal(data, &prompt) == nil && strings.TrimSpace(prompt.WorkingDirectory) != "" {
			return prompt.WorkingDirectory
		}
	}
	return ref.Workspace
}

// readGrokSummary re-reads summary.json from disk. The store nils Summary when the three display
// fields are empty, but the handoff list needs session_kind and head_branch which may still be set.
func readGrokSummary(sourcePath string) groksession.Summary {
	data, err := os.ReadFile(filepath.Join(sourcePath, "summary.json"))
	if err != nil {
		return groksession.Summary{}
	}
	var s groksession.Summary
	if json.Unmarshal(data, &s) != nil {
		return groksession.Summary{}
	}
	return s
}

// readGrokFirstPrompt returns the first thing a person typed in a Grok chat history.
func readGrokFirstPrompt(path string) string {
	var prompt string
	scanHead(path, func(line []byte) bool {
		var msg struct {
			Type            string          `json:"type"`
			Content         json.RawMessage `json:"content"`
			SyntheticReason string          `json:"synthetic_reason"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.Type != "user" || msg.SyntheticReason != "" {
			return false
		}
		prompt = piContentText(msg.Content)
		return prompt != ""
	})
	return prompt
}
