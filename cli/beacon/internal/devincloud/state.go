package devincloud

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// State tracks, per org, which session events have already been emitted so that
// re-polling is idempotent. It is a small JSON file managed by the connector.
type State struct {
	Sessions map[string]*SessionState `json:"sessions"`
}

// SessionState records progress for one session.
type SessionState struct {
	UpdatedAt    int64           `json:"updated_at"`
	Status       string          `json:"status"`
	Done         bool            `json:"done"`          // final status; skip henceforth
	EndedEmitted bool            `json:"ended_emitted"` // session.ended emitted for the current terminal episode
	UploadedAt   int64           `json:"uploaded_at"`   // updated_at at which this session's snapshot was last uploaded to GCS
	Emitted      map[string]bool `json:"emitted"`       // dedup ids for started + message events
}

// LoadState reads state from path. A missing file yields empty state. An empty
// path yields empty, non-persisted state.
func LoadState(path string) (*State, error) {
	s := &State{Sessions: map[string]*SessionState{}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*SessionState{}
	}
	return s, nil
}

// Save writes state to path atomically. A nil/empty path is a no-op.
func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
