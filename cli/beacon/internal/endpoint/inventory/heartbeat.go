package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

const stateFileName = "inventory-state.json"
const LogFileName = "inventory_state.jsonl"

type Counts struct {
	Configs    int `json:"configs"`
	MCPServers int `json:"mcp_servers"`
	Skills     int `json:"skills"`
}

// State is what the scheduled heartbeat remembers between runs. A run that fails to append
// leaves it untouched, so the next run is a plain retry.
type State struct {
	LastEmittedAt      string `json:"last_emitted_at,omitempty"`
	LastSnapshotDigest string `json:"last_snapshot_digest,omitempty"`
	// LastSnapshotDigests remembers the last digest per scanned home (keyed by UserScope.HomeHash).
	// Inventory is per user but the state file is per endpoint: on a Mac where the console user
	// changes between runs, a single digest would flip on every switch and write a snapshot each
	// time although nobody's inventory changed. LastSnapshotDigest keeps the most recent value
	// for status and for state files written before this map existed.
	LastSnapshotDigests map[string]string `json:"last_snapshot_digests,omitempty"`
}

// DigestFor returns the last digest recorded for a home, falling back to the endpoint-wide value
// when no per-home record exists yet (a state file from an older version).
func (s State) DigestFor(homeHash string) string {
	if d, ok := s.LastSnapshotDigests[homeHash]; ok && homeHash != "" {
		return d
	}
	if len(s.LastSnapshotDigests) == 0 {
		return s.LastSnapshotDigest
	}
	return ""
}

// WithDigest returns a copy of the state recording digest for a home and as the latest overall.
func (s State) WithDigest(homeHash, digest string) State {
	out := s
	out.LastSnapshotDigest = digest
	out.LastSnapshotDigests = make(map[string]string, len(s.LastSnapshotDigests)+1)
	for k, v := range s.LastSnapshotDigests {
		out.LastSnapshotDigests[k] = v
	}
	if homeHash != "" {
		out.LastSnapshotDigests[homeHash] = digest
	}
	return out
}

type LockedState struct {
	path string
	file *os.File
	lock *filelock.Lock
}

func StatePath(userMode bool) string {
	return filepath.Join(endpointconfig.BaseDir(userMode), stateFileName)
}

func StatePathForLog(runtimeLogPath string, userMode bool) string {
	return filepath.Join(filepath.Dir(LogPath(runtimeLogPath, userMode)), stateFileName)
}

func LogPath(runtimeLogPath string, userMode bool) string {
	if runtimeLogPath != "" {
		return filepath.Join(filepath.Dir(runtimeLogPath), LogFileName)
	}
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", ".beacon", "endpoint", "logs", LogFileName)
		}
		return filepath.Join(home, ".beacon", "endpoint", "logs", LogFileName)
	}
	return filepath.Join(endpointconfig.SystemLogDir(), LogFileName)
}

func LockState(path string) (*LockedState, State, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, State{}, err
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, State{}, err
	}
	held, err := filelock.Exclusive(file)
	if err != nil {
		_ = file.Close()
		return nil, State{}, err
	}
	state, err := ReadState(path)
	if err != nil {
		_ = held.Release()
		_ = file.Close()
		return nil, State{}, err
	}
	return &LockedState{path: path, file: file, lock: held}, state, nil
}

func (s *LockedState) Save(state State) error {
	if s == nil {
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *LockedState) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	unlockErr := s.lock.Release()
	closeErr := s.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func ReadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func CountsFor(result Result) Counts {
	return Counts{
		Configs:    countExistingConfigs(result.Configs),
		MCPServers: len(result.MCPServers),
		Skills:     countExistingSkills(result.Skills),
	}
}

func SnapshotDigest(result Result) string {
	payload := struct {
		Configs    []Config    `json:"configs"`
		MCPServers []MCPServer `json:"mcp_servers"`
		Skills     []Skill     `json:"skills"`
		UserScope  UserScope   `json:"user_scope"`
	}{
		Configs:    existingConfigs(result.Configs),
		MCPServers: result.MCPServers,
		Skills:     existingSkills(result.Skills),
		UserScope:  result.UserScope,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return hashString(result.GeneratedAt)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func existingConfigs(configs []Config) []Config {
	out := make([]Config, 0, len(configs))
	for _, config := range configs {
		if config.Exists {
			out = append(out, config)
		}
	}
	return out
}

func existingSkills(skills []Skill) []Skill {
	out := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.Exists {
			out = append(out, skill)
		}
	}
	return out
}

func countExistingConfigs(configs []Config) int {
	count := 0
	for _, config := range configs {
		if config.Exists {
			count++
		}
	}
	return count
}

func countExistingSkills(skills []Skill) int {
	count := 0
	for _, skill := range skills {
		if skill.Exists {
			count++
		}
	}
	return count
}
