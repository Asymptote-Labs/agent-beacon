package hermessession

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionfs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
)

// SessionHomesFileName is the file, beside the cursor files, that records which Hermes home holds
// the unkeyed event ids of each session id.
const SessionHomesFileName = "hermes-session-homes.json"

const sessionHomesVersion = 1

// sessionHomes maps a Hermes session id to the canonical path of the home whose sweeps write that
// session's unkeyed event ids. Every home a sweep of one cursor directory reads shares it, so the
// first home to collect a session id keeps the ids Beacon has always derived, and any other home
// that later finds the same id, as a cloned profile does, keys its ids by its own path instead.
type sessionHomes struct {
	Version  int               `json:"version"`
	Sessions map[string]string `json:"sessions"`
}

var sessionHomesFile = sessionsync.StateFile[sessionHomes]{
	Name: "Hermes session home",
	New: func() *sessionHomes {
		return &sessionHomes{Version: sessionHomesVersion, Sessions: map[string]string{}}
	},
	Valid: func(s *sessionHomes) bool { return s.Version == sessionHomesVersion && s.Sessions != nil },
	Stamp: func(s *sessionHomes) { s.Version = sessionHomesVersion },
}

// SessionHomesPath is the session home file for the cursor at statePath, or empty when there is no
// cursor file, as for --print.
func SessionHomesPath(statePath string) string {
	if statePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(statePath), SessionHomesFileName)
}

// assignEventIDHomes decides, for each session this sweep reads, whether its event ids are keyed by
// home, and records the decision on its cursor. It claims a session id for home when no home holds
// it yet, so the common case, a session id only one home has, keeps its unkeyed ids.
//
// A cursor that has already collected events keeps the ids it wrote: a session a home collected
// before this file existed stays unkeyed, and is claimed for that home when no other home was first.
// Only a session's first sweep can be keyed, so ids never change part way through a session.
//
// A claim held by a home that no longer has a state.db was left by a home that was renamed or moved,
// as `hermes profile rename` does, or deleted after it was copied. The home that finds the session
// next takes the claim and keeps the unkeyed ids, rather than upload the same history again under
// new ids.
//
// path is the shared session home file; an empty path decides nothing, leaving every id unkeyed.
func assignEventIDHomes(path, home string, sessions []Session, state *State) error {
	if path == "" || len(sessions) == 0 {
		return nil
	}
	return sessionHomesFile.Update(path, func(homes *sessionHomes) bool {
		changed := false
		for _, session := range sessions {
			cursor := state.cursor(session.ID)
			owner, claimed := homes.Sessions[session.ID]
			switch {
			case cursor.Started || cursor.LastMessageID > 0:
				if !claimed && !cursor.KeyedByHome {
					homes.Sessions[session.ID] = home
					changed = true
				}
			case !claimed || (owner != home && homeStoreGone(owner)):
				homes.Sessions[session.ID] = home
				changed = true
				cursor.KeyedByHome = false
			default:
				cursor.KeyedByHome = owner != home
			}
		}
		return changed
	})
}

// homeStoreGone reports whether the Hermes home dir no longer has a state.db. A store that cannot be
// checked for another reason counts as present, so the claim stays with it.
func homeStoreGone(dir string) bool {
	_, err := sessionfs.Stat(filepath.Join(dir, "state.db"))
	return errors.Is(err, fs.ErrNotExist)
}
