package pisession

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func LoadState(path string) (*State, error) {
	state := &State{Version: StateVersion, Files: map[string]*Cursor{}}
	if path == "" {
		return state, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return nil, err
	}
	var stored State
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("read Pi collector state %s: %w", path, err)
	}
	if stored.Version != StateVersion || stored.Files == nil {
		return state, nil
	}
	state.Files = stored.Files
	return state, nil
}

func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	s.Version = StateVersion
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
