package devincloud

import "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"

// State tracks, per org, which session events have already been emitted so that
// re-polling is idempotent. It is a small JSON file managed by the connector.
type State struct {
	Sessions map[string]*SessionState `json:"sessions"`
}

// SessionState records progress for one session.
type SessionState struct {
	UpdatedAt    int64           `json:"updated_at"`
	Status       string          `json:"status"`
	SyncedAt     int64           `json:"synced_at"`     // updated_at at which a sweep last FULLY processed this session; 0 while incomplete
	Done         bool            `json:"done"`          // final status; skip henceforth
	EndedEmitted bool            `json:"ended_emitted"` // session.ended emitted for the current terminal episode
	UploadedAt   int64           `json:"uploaded_at"`   // updated_at at which this session's snapshot was last uploaded to GCS
	Emitted      map[string]bool `json:"emitted"`       // dedup ids for started + message events
}

// LoadState reads state from path. A missing file yields empty state. An empty
// path yields empty, non-persisted state.
func LoadState(path string) (*State, error) { return stateFile.Load(path) }

// Save writes state to path atomically. A nil/empty path is a no-op.
func (s *State) Save(path string) error { return stateFile.Save(path, s) }

// stateFile has no version and no collector name: an unreadable file returns the decode error as is.
var stateFile = sessionsync.StateFile[State]{
	New:   func() *State { return &State{Sessions: map[string]*SessionState{}} },
	Valid: func(s *State) bool { return s.Sessions != nil },
}

func (s *State) get(sessionID string) *SessionState {
	ss := s.Sessions[sessionID]
	if ss == nil {
		ss = &SessionState{Emitted: map[string]bool{}}
		s.Sessions[sessionID] = ss
	}
	if ss.Emitted == nil {
		ss.Emitted = map[string]bool{}
	}
	return ss
}
