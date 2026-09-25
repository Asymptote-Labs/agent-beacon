package handoff

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/claudesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/clinesession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/codexsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/opencodesession"
)

// Harness names, as the endpoint event schema spells them.
const (
	HarnessClaude   = claudesession.Harness
	HarnessCodex    = codexsession.Harness
	HarnessOpenCode = opencodesession.Harness
	HarnessCline    = clinesession.Harness
)

// Harnesses lists the runtimes handoff supports, in display order.
var Harnesses = []string{HarnessClaude, HarnessCodex, HarnessOpenCode, HarnessCline}

var harnessAliases = map[string]string{
	"claude":      HarnessClaude,
	"claude-code": HarnessClaude,
	"claude_code": HarnessClaude,
	"codex":       HarnessCodex,
	"codex-cli":   HarnessCodex,
	"codex_cli":   HarnessCodex,
	"opencode":    HarnessOpenCode,
	"cline":       HarnessCline,
}

// ParseHarness resolves a user-supplied runtime name to its harness name. An empty name stays
// empty, meaning every runtime.
func ParseHarness(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", nil
	}
	if harness, ok := harnessAliases[name]; ok {
		return harness, nil
	}
	return "", fmt.Errorf("unsupported runtime %q (supported: claude, codex, opencode, cline)", name)
}

// StoreDirs overrides where each runtime's session store is read from. An empty field means the
// runtime's default location.
type StoreDirs struct {
	ClaudeProjects string
	Codex          string
	OpenCode       string
	Cline          string
}

// DefaultSources returns a source for every supported runtime.
func DefaultSources(dirs StoreDirs) []Source {
	return []Source{
		&claudeSource{dir: dirs.ClaudeProjects},
		&codexSource{dir: dirs.Codex},
		&openCodeSource{dir: dirs.OpenCode},
		&clineSource{dir: dirs.Cline},
	}
}

func unixMS(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

type claudeSource struct{ dir string }

func (s *claudeSource) Harness() string { return HarnessClaude }

func (s *claudeSource) store() (*claudesession.Store, error) { return claudesession.NewStore(s.dir) }

func (s *claudeSource) List() ([]Session, error) {
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
			Harness:    HarnessClaude,
			ID:         ref.ID,
			Directory:  ref.ProjectPath,
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
			Subagent:   ref.IsSidechain,
			ParentID:   ref.ParentSessionID,
		}
		var indexPath string
		if ref.Index != nil {
			indexPath = ref.Index.ProjectPath
			session.Title = oneLine(firstNonEmpty(ref.Index.Summary, ref.Index.FirstPrompt))
			session.Branch = ref.Index.GitBranch
		}
		if indexPath == "" || session.Title == "" || session.Branch == "" {
			head := readClaudeHead(ref.Path)
			if indexPath == "" && head.CWD != "" {
				session.Directory = head.CWD
			}
			session.Title = firstNonEmpty(session.Title, oneLine(head.FirstPrompt))
			session.Branch = firstNonEmpty(session.Branch, head.GitBranch)
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

type codexSource struct{ dir string }

func (s *codexSource) Harness() string { return HarnessCodex }

func (s *codexSource) store() (*codexsession.Store, error) { return codexsession.NewStore(s.dir) }

func (s *codexSource) List() ([]Session, error) {
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
	// A thread can own more than one rollout file, each carrying the thread's id. The newest file
	// stands for the thread.
	newest := map[string]codexsession.SessionRef{}
	for _, ref := range refs {
		if current, ok := newest[ref.ID]; !ok || ref.ModTimeUnixMS >= current.ModTimeUnixMS {
			newest[ref.ID] = ref
		}
	}
	sessions := make([]Session, 0, len(newest))
	for _, ref := range newest {
		session := Session{
			Harness:    HarnessCodex,
			ID:         ref.ID,
			Directory:  ref.Workspace,
			SourcePath: ref.Path,
			UpdatedAt:  unixMS(ref.ModTimeUnixMS),
		}
		if ref.Index != nil {
			session.Title = oneLine(ref.Index.ThreadName)
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

type openCodeSource struct{ dir string }

func (s *openCodeSource) Harness() string { return HarnessOpenCode }

func (s *openCodeSource) store() (*opencodesession.Store, error) {
	return opencodesession.NewStore(s.dir)
}

func (s *openCodeSource) List() ([]Session, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	if !store.Exists() {
		return nil, nil
	}
	// OpenCode reads two stores; List returns what it could read alongside any error.
	refs, err := store.List()
	sessions := make([]Session, 0, len(refs))
	for _, ref := range refs {
		sessions = append(sessions, Session{
			Harness:    HarnessOpenCode,
			ID:         ref.ID,
			Title:      oneLine(firstNonEmpty(ref.Title, ref.Preview)),
			Directory:  ref.Directory,
			SourcePath: ref.SourcePath,
			Store:      string(ref.Kind),
			UpdatedAt:  unixMS(ref.UpdatedAtUnixMS),
			Subagent:   ref.RelationshipType == "subagent",
			ParentID:   relatedParent(ref.RelationshipType, ref.RelatedTo),
		})
	}
	return sessions, err
}

type clineSource struct{ dir string }

func (s *clineSource) Harness() string { return HarnessCline }

func (s *clineSource) store() (*clinesession.Store, error) { return clinesession.NewStore(s.dir) }

func (s *clineSource) List() ([]Session, error) {
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
	// A task can be stored both as a CLI session and in the older task history under one id. The
	// CLI session is the copy `cline --id` reopens, so it wins.
	byID := map[string]Session{}
	for _, ref := range refs {
		// Kanban cards are Cline's board view of work, not sessions the CLI can reopen.
		if ref.Kind == clinesession.SourceKanban {
			continue
		}
		if prev, ok := byID[ref.ID]; ok && prev.Store == clinesession.SourceMessages {
			continue
		}
		byID[ref.ID] = Session{
			Harness:    HarnessCline,
			ID:         ref.ID,
			Title:      oneLine(firstNonEmpty(ref.Title, ref.Preview)),
			Directory:  ref.Directory,
			SourcePath: ref.SourcePath,
			UpdatedAt:  unixMS(ref.UpdatedAtUnixMS),
			Store:      ref.Kind,
			Subagent:   ref.RelationshipType == "subagent",
			ParentID:   relatedParent(ref.RelationshipType, ref.RelatedTo),
		}
	}
	sessions := make([]Session, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

func relatedParent(relationship, relatedTo string) string {
	if relationship != "subagent" {
		return ""
	}
	parent, _, _ := strings.Cut(relatedTo, "#")
	return parent
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
