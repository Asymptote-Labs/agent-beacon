// Package handoff finds agent sessions on this machine so they can be picked up again, in the same
// runtime or a different one.
//
// A handoff reads the runtime's own session store, never Beacon's runtime log alone: the store is
// what the runtime itself resumes from, and it keeps content that the runtime log truncates. Every
// read is local and read-only.
package handoff

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is one resumable agent session as its runtime recorded it.
type Session struct {
	Harness    string `json:"harness"`
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Directory  string `json:"directory,omitempty"`
	Branch     string `json:"branch,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	// Store names which of a runtime's stores holds the session when it has more than one, such as
	// Cline's CLI sessions and its older task history.
	Store     string    `json:"store,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Subagent marks a child session a runtime spawned for delegated work. Those are listed only on
	// request, because resuming one resumes a fragment of the parent's task.
	Subagent bool   `json:"subagent,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

// Source lists the sessions one runtime has stored locally.
type Source interface {
	Harness() string
	List() ([]Session, error)
}

// Filter narrows a session listing. The zero value lists every top-level session.
type Filter struct {
	Harness          string
	Directory        string
	IncludeSubagents bool
	Limit            int
}

// SourceError reports a runtime whose store could not be read. The other runtimes still list.
type SourceError struct {
	Harness string
	Err     error
}

func (e *SourceError) Error() string { return fmt.Sprintf("%s: %v", e.Harness, e.Err) }
func (e *SourceError) Unwrap() error { return e.Err }

// List returns the sessions every source has stored, newest first. A source that fails is reported
// in the returned error while the sessions from the others are still returned.
func List(sources []Source, filter Filter) ([]Session, error) {
	var sessions []Session
	var errs []error
	for _, source := range sources {
		if filter.Harness != "" && source.Harness() != filter.Harness {
			continue
		}
		found, err := source.List()
		if err != nil {
			errs = append(errs, &SourceError{Harness: source.Harness(), Err: err})
		}
		for _, session := range found {
			if session.ID == "" {
				continue
			}
			if session.Subagent && !filter.IncludeSubagents {
				continue
			}
			if filter.Directory != "" && !withinDirectory(session.Directory, filter.Directory) {
				continue
			}
			sessions = append(sessions, session)
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if !sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
		}
		if sessions[i].Harness != sessions[j].Harness {
			return sessions[i].Harness < sessions[j].Harness
		}
		return sessions[i].ID < sessions[j].ID
	})
	if filter.Limit > 0 && len(sessions) > filter.Limit {
		sessions = sessions[:filter.Limit]
	}
	return sessions, errors.Join(errs...)
}

// MinPrefixLength is the shortest session id prefix Find accepts, so a stray short argument cannot
// silently pick one of many sessions.
const MinPrefixLength = 6

// ErrNotFound reports that no stored session has the requested id.
var ErrNotFound = errors.New("session not found")

// AmbiguousError reports an id or prefix that names more than one stored session.
type AmbiguousError struct {
	ID         string
	Candidates []Session
}

func (e *AmbiguousError) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, candidate := range e.Candidates {
		names = append(names, candidate.Harness+" "+candidate.ID)
	}
	return fmt.Sprintf("session %q matches %d sessions (%s); pass the full id or --harness", e.ID, len(e.Candidates), strings.Join(names, ", "))
}

// Find returns the one session whose id is id, or, failing an exact match, the one session whose id
// starts with id. Subagent sessions are searched too, since naming one explicitly is deliberate.
func Find(sources []Source, harness, id string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, errors.New("session id is required")
	}
	sessions, err := List(sources, Filter{Harness: harness, IncludeSubagents: true})
	var exact, prefixed []Session
	for _, session := range sessions {
		switch {
		case session.ID == id:
			exact = append(exact, session)
		case len(id) >= MinPrefixLength && strings.HasPrefix(session.ID, id):
			prefixed = append(prefixed, session)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = prefixed
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if err != nil {
			return Session{}, fmt.Errorf("%w: %s (some session stores could not be read: %v)", ErrNotFound, id, err)
		}
		return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	default:
		return Session{}, &AmbiguousError{ID: id, Candidates: matches}
	}
}

// withinDirectory reports whether a session recorded in dir ran in root or below it.
func withinDirectory(dir, root string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	root = filepath.Clean(root)
	if dir == root {
		return true
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
