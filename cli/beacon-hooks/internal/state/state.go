package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/filelock"
)

var stateMutex sync.Mutex

// lockState serializes access to the platform's state file. Hooks run as separate processes, one per
// event, so the in-process mutex is not enough: without the file lock two concurrent hooks read the same
// state and the later write drops the earlier one's change. The lock is advisory, taken on a sibling
// file so that replacing state.json never invalidates it. If the lock file cannot be opened the caller
// proceeds without cross-process exclusion, as before, rather than failing a hook.
func lockState(platform string) (unlock func()) {
	stateMutex.Lock()
	if err := config.EnsureStateDir(platform); err != nil {
		return stateMutex.Unlock
	}
	file, err := os.OpenFile(filepath.Join(config.GetStateDir(platform), "state.json.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return stateMutex.Unlock
	}
	held, err := filelock.Exclusive(file)
	if err != nil {
		_ = file.Close()
		return stateMutex.Unlock
	}
	return func() {
		_ = held.Release()
		_ = file.Close()
		stateMutex.Unlock()
	}
}

// writeStateFile replaces path through a uniquely named temporary file, so concurrent writers never
// share one and a reader never sees a partial file.
func writeStateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "state.json.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

const (
	staleSessionTTLHours = 2
	// replyMarkerTTL is how long a session's last recorded reply is remembered. A Stop that
	// re-fires over an unchanged transcript after that writes the reply again, which costs one
	// duplicate event rather than an unbounded state file.
	replyMarkerTTL = 14 * 24 * time.Hour
	// promptMarkerTTL is how long a session's first-prompt marker is remembered. Antigravity
	// recovers a session's first prompt from its transcript on the first tool call, and a
	// conversation resumed hours later still starts with that prompt; while the marker is kept it
	// is not recorded again. Past the TTL a resumed conversation records it once more, which costs
	// one duplicate event rather than an unbounded state file.
	promptMarkerTTL = 14 * 24 * time.Hour
)

// SessionData represents data for a single session. Older state files also carry evaluations,
// reprompt_count and model keys; they are ignored on load and dropped on the next save.
type SessionData struct {
	PromptEmitted bool `json:"prompt_emitted,omitempty"`
	// PromptEmittedAt is when PromptEmitted was set. Stale-session cleanup keeps a session whose
	// prompt marker is younger than promptMarkerTTL. Markers written before this field existed
	// carry none and are aged from CreatedAt.
	PromptEmittedAt string `json:"prompt_emitted_at,omitempty"`
	// LastReplyID names the last assistant reply a hook recorded for this session, so a reply read
	// from a transcript that has not moved since the previous turn is not recorded twice.
	LastReplyID string `json:"last_reply_id,omitempty"`
	// LastReplyAt is when LastReplyID was recorded. Stale-session cleanup keeps a session whose
	// reply marker is younger than replyMarkerTTL, so the dedupe outlives the two-hour window
	// that every other session is cleaned up on.
	LastReplyAt string `json:"last_reply_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// SessionState manages session state
type SessionState struct {
	sessionID     string
	stateFilePath string
	platform      string
}

// NewSessionState creates a new session state manager
func NewSessionState(sessionID, platform string) *SessionState {
	return &SessionState{
		sessionID:     sessionID,
		stateFilePath: filepath.Join(config.GetStateDir(platform), "state.json"),
		platform:      platform,
	}
}

func (s *SessionState) loadStateFile() map[string]*SessionData {
	data, err := os.ReadFile(s.stateFilePath)
	if err != nil {
		return make(map[string]*SessionData)
	}

	var state map[string]*SessionData
	if err := json.Unmarshal(data, &state); err != nil {
		return make(map[string]*SessionData)
	}

	return state
}

func (s *SessionState) saveStateFile(state map[string]*SessionData) error {
	if err := config.EnsureStateDir(s.platform); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return writeStateFile(s.stateFilePath, data)
}

func (s *SessionState) ensureSession(state map[string]*SessionData) {
	if state[s.sessionID] == nil {
		state[s.sessionID] = &SessionData{
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
}

// Register records the session so CleanupStaleForPlatform can remove its log if session-end never runs
func (s *SessionState) Register() error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	if state[s.sessionID] != nil {
		return nil
	}
	s.ensureSession(state)
	return s.saveStateFile(state)
}

func (s *SessionState) HasPromptEmitted() bool {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.PromptEmitted
	}
	return false
}

func (s *SessionState) SetPromptEmitted() error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].markPromptEmitted()
	return s.saveStateFile(state)
}

func (s *SessionState) MarkPromptEmittedIfNeeded() (bool, error) {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	if state[s.sessionID].PromptEmitted {
		return false, nil
	}
	state[s.sessionID].markPromptEmitted()
	if err := s.saveStateFile(state); err != nil {
		return false, err
	}
	return true, nil
}

func (d *SessionData) markPromptEmitted() {
	d.PromptEmitted = true
	d.PromptEmittedAt = time.Now().UTC().Format(time.RFC3339)
}

// MarkReplyRecorded records id as the session's last recorded assistant reply. It reports false,
// and changes nothing, when id is already the last one recorded, so a caller writes a reply at
// most once even when two Stop hooks of one session race.
func (s *SessionState) MarkReplyRecorded(id string) (bool, error) {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	if state[s.sessionID].LastReplyID == id {
		return false, nil
	}
	state[s.sessionID].LastReplyID = id
	state[s.sessionID].LastReplyAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveStateFile(state); err != nil {
		return false, err
	}
	return true, nil
}

// CleanupStaleForPlatform removes stale sessions for the given platform
func CleanupStaleForPlatform(platform string) int {
	defer lockState(platform)()

	stateFilePath := filepath.Join(config.GetStateDir(platform), "state.json")

	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		return 0
	}

	var state map[string]*SessionData
	if err := json.Unmarshal(data, &state); err != nil {
		return 0
	}

	if len(state) == 0 {
		return 0
	}

	cutoff := time.Now().Add(-time.Duration(staleSessionTTLHours) * time.Hour)
	sessionsToRemove := []string{}

	for sessionID, sessionData := range state {
		if sessionData.hasRecentReplyMarker() || sessionData.hasRecentPromptMarker() {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, sessionData.CreatedAt)
		if err != nil || createdAt.Before(cutoff) {
			sessionsToRemove = append(sessionsToRemove, sessionID)
		}
	}

	for _, sessionID := range sessionsToRemove {
		delete(state, sessionID)
		// Remove orphan session log file
		os.Remove(config.GetSessionLogFile(platform, sessionID))
	}

	if len(sessionsToRemove) > 0 {
		config.EnsureStateDir(platform)
		jsonData, err := json.MarshalIndent(state, "", "  ")
		if err == nil {
			_ = writeStateFile(stateFilePath, jsonData)
		}
	}

	return len(sessionsToRemove)
}

// hasRecentReplyMarker reports whether the session recorded a reply within replyMarkerTTL.
func (d *SessionData) hasRecentReplyMarker() bool {
	if d.LastReplyID == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, d.LastReplyAt)
	return err == nil && time.Since(at) < replyMarkerTTL
}

// hasRecentPromptMarker reports whether the session's first-prompt marker was set within
// promptMarkerTTL, aging a marker that predates PromptEmittedAt from CreatedAt.
func (d *SessionData) hasRecentPromptMarker() bool {
	if !d.PromptEmitted {
		return false
	}
	stamp := d.PromptEmittedAt
	if stamp == "" {
		stamp = d.CreatedAt
	}
	at, err := time.Parse(time.RFC3339, stamp)
	return err == nil && time.Since(at) < promptMarkerTTL
}
