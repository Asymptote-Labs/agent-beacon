package handoff

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/cursorsession"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// Cursor keeps sessions in two stores: Composer conversations in its global storage SQLite
// database, and agent transcripts under ~/.cursor/projects, which the IDE and the cursor-agent CLI
// both write. One conversation can be in both under the same id.

type cursorSource struct{ dir string }

func (s *cursorSource) Harness() string { return HarnessCursor }

// store reads dir as Cursor's projects directory, as `beacon endpoint cursor sync --projects-dir`
// does. The global storage database is always read from its default location.
func (s *cursorSource) store() (*cursorsession.Store, error) {
	return cursorsession.NewStore("", s.dir)
}

func (s *cursorSource) List() ([]Session, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	if !store.Exists() {
		return nil, nil
	}
	// Cursor has two stores; List returns what it could read alongside any error.
	refs, err := store.List()
	byID := map[string]Session{}
	for _, ref := range refs {
		session := Session{
			Harness:    HarnessCursor,
			ID:         ref.ID,
			Title:      oneLine(firstNonEmpty(ref.Title, ref.Preview)),
			Directory:  ref.Workspace,
			SourcePath: ref.SourcePath,
			Store:      string(ref.Kind),
			UpdatedAt:  unixMS(ref.UpdatedAtUnixMS),
			Subagent:   ref.RelationshipType == "subagent",
			ParentID:   relatedParent(ref.RelationshipType, ref.RelatedTo),
		}
		// A conversation stored twice is listed once: as the transcript when cursor-agent can
		// reopen it, else as the Composer record, which is Cursor's own conversation store.
		if prev, ok := byID[ref.ID]; ok && !cursorPrefer(session, prev) {
			continue
		}
		byID[ref.ID] = session
	}
	sessions := make([]Session, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, err
}

func cursorPrefer(candidate, current Session) bool {
	if cursorCLIChat(current) {
		return false
	}
	return cursorCLIChat(candidate) || candidate.Store == string(cursorsession.SourceGlobalStorage)
}

func (s *cursorSource) Events(session Session) ([]schema.Event, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	refs, listErr := store.List()
	for _, ref := range refs {
		if ref.ID != session.ID || ref.SourcePath != session.SourcePath {
			continue
		}
		records, err := store.Read(ref)
		if err != nil {
			return nil, err
		}
		mapped := cursorsession.MapTrace(ref, records, cursorsession.MapOptions{})
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

// cursorCLIChat reports whether session is a chat cursor-agent can reopen with --resume. The CLI
// keeps each chat it started in chats/<md5 of the directory it ran in>/<chat id>/store.db under its
// config directory, and resumes a chat id from the directory it is started in. IDE conversations
// have no such store, so only a transcript with one is resumable.
func cursorCLIChat(session Session) bool {
	if session.Store != string(cursorsession.SourceTranscript) || session.Subagent || session.ID == "" || session.Directory == "" {
		return false
	}
	root := cursorConfigDir()
	if root == "" {
		return false
	}
	// The CLI hashes its working directory as the operating system reports it, symlinks resolved.
	dir := session.Directory
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	sum := md5.Sum([]byte(dir))
	info, err := os.Stat(filepath.Join(root, "chats", hex.EncodeToString(sum[:]), session.ID, "store.db"))
	return err == nil && info.Mode().IsRegular()
}

// cursorConfigDir is cursor-agent's config directory: CURSOR_CONFIG_DIR, else
// $XDG_CONFIG_HOME/cursor, else ~/.cursor.
func cursorConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CURSOR_CONFIG_DIR")); dir != "" {
		return dir
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "cursor")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".cursor")
}
