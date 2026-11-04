package pisession

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/sessionsync"
)

const StateVersion = 1

type Cursor struct {
	LastLine  int   `json:"last_line"`
	Started   bool  `json:"started"`
	ModTimeMS int64 `json:"mod_time_ms,omitempty"`
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

type State struct {
	Version int                `json:"version"`
	Files   map[string]*Cursor `json:"files"`
}

var stateFile = sessionsync.StateFile[State]{
	Name:  "Pi",
	New:   func() *State { return &State{Version: StateVersion, Files: map[string]*Cursor{}} },
	Valid: func(s *State) bool { return s.Version == StateVersion && s.Files != nil },
	Stamp: func(s *State) { s.Version = StateVersion },
}

func LoadState(path string) (*State, error) { return stateFile.Load(path) }

func (s *State) Save(path string) error { return stateFile.Save(path, s) }

func (s *State) cursor(path string) *Cursor {
	if s.Files == nil {
		s.Files = map[string]*Cursor{}
	}
	key := stateKey(path)
	cursor := s.Files[key]
	if cursor == nil {
		cursor = &Cursor{}
		s.Files[key] = cursor
	}
	return cursor
}

func stateKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func DefaultStatePath() string {
	base := filepath.Join(os.TempDir(), "beacon")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		base = filepath.Join(home, ".beacon")
	}
	return filepath.Join(base, "endpoint", "state", "pi.json")
}
