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
	staleEvaluationTTLHours = 2
)

// Evaluation represents a pending evaluation
type Evaluation struct {
	EvaluationID string `json:"evaluation_id"`
	FilePath     string `json:"file_path"`
	CreatedAt    string `json:"created_at"`
}

// SessionData represents data for a single session
type SessionData struct {
	Evaluations   []Evaluation `json:"evaluations"`
	RepromptCount int          `json:"reprompt_count"`
	Model         string       `json:"model,omitempty"`
	PromptEmitted bool         `json:"prompt_emitted,omitempty"`
	CreatedAt     string       `json:"created_at,omitempty"`
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
			Evaluations:   []Evaluation{},
			RepromptCount: 0,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		}
	}
}

// SetModel stores the AI model name for this session
func (s *SessionState) SetModel(model string) error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].Model = model
	return s.saveStateFile(state)
}

// GetModel returns the AI model name for this session
func (s *SessionState) GetModel() string {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.Model
	}
	return ""
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
	state[s.sessionID].PromptEmitted = true
	return s.saveStateFile(state)
}

func (s *SessionState) MarkPromptEmittedIfNeeded() (bool, error) {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	if state[s.sessionID].PromptEmitted {
		return false, nil
	}
	state[s.sessionID].PromptEmitted = true
	if err := s.saveStateFile(state); err != nil {
		return false, err
	}
	return true, nil
}

// AddEvaluation adds a new pending evaluation
func (s *SessionState) AddEvaluation(evaluationID, filePath string) error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)

	state[s.sessionID].Evaluations = append(state[s.sessionID].Evaluations, Evaluation{
		EvaluationID: evaluationID,
		FilePath:     filePath,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	})

	return s.saveStateFile(state)
}

// GetPendingEvaluations returns pending evaluations for this session
func (s *SessionState) GetPendingEvaluations() []Evaluation {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.Evaluations
	}
	return []Evaluation{}
}

// ClearEvaluations removes all evaluations for this session
func (s *SessionState) ClearEvaluations() error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].Evaluations = []Evaluation{}
	return s.saveStateFile(state)
}

// GetRepromptCount returns the current re-prompt count
func (s *SessionState) GetRepromptCount() int {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.RepromptCount
	}
	return 0
}

// IncrementRepromptCount increments and returns the re-prompt count
func (s *SessionState) IncrementRepromptCount() (int, error) {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].RepromptCount++
	newCount := state[s.sessionID].RepromptCount
	if err := s.saveStateFile(state); err != nil {
		return 0, err
	}
	return newCount, nil
}

// ResetRepromptCount resets the re-prompt count to 0
func (s *SessionState) ResetRepromptCount() error {
	defer lockState(s.platform)()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].RepromptCount = 0
	return s.saveStateFile(state)
}

// CleanupStaleForPlatform removes stale evaluations for the given platform
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

	cutoff := time.Now().Add(-time.Duration(staleEvaluationTTLHours) * time.Hour)
	removedCount := 0
	sessionsToRemove := []string{}

	for sessionID, sessionData := range state {
		var freshEvals []Evaluation
		for _, eval := range sessionData.Evaluations {
			createdAt, err := time.Parse(time.RFC3339, eval.CreatedAt)
			if err != nil || createdAt.After(cutoff) {
				freshEvals = append(freshEvals, eval)
			} else {
				removedCount++
			}
		}
		sessionData.Evaluations = freshEvals

		// Mark empty sessions for removal only if they are stale
		if len(freshEvals) == 0 && sessionData.RepromptCount == 0 {
			createdAt, err := time.Parse(time.RFC3339, sessionData.CreatedAt)
			if err != nil || createdAt.Before(cutoff) {
				sessionsToRemove = append(sessionsToRemove, sessionID)
			}
		}
	}

	for _, sessionID := range sessionsToRemove {
		delete(state, sessionID)
		// Remove orphan session log file
		os.Remove(config.GetSessionLogFile(platform, sessionID))
	}

	if removedCount > 0 || len(sessionsToRemove) > 0 {
		config.EnsureStateDir(platform)
		jsonData, err := json.MarshalIndent(state, "", "  ")
		if err == nil {
			_ = writeStateFile(stateFilePath, jsonData)
		}
	}

	return removedCount
}
