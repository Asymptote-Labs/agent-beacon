package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/config"
)

var stateMutex sync.Mutex

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
	config.EnsureStateDir(s.platform)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Write to temp file first, then rename (atomic)
	tmpPath := s.stateFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.stateFilePath)
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
func (s *SessionState) SetModel(model string) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].Model = model
	s.saveStateFile(state)
}

// GetModel returns the AI model name for this session
func (s *SessionState) GetModel() string {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.Model
	}
	return ""
}

func (s *SessionState) HasPromptEmitted() bool {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.PromptEmitted
	}
	return false
}

func (s *SessionState) SetPromptEmitted() {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].PromptEmitted = true
	s.saveStateFile(state)
}

func (s *SessionState) MarkPromptEmittedIfNeeded() bool {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	if state[s.sessionID].PromptEmitted {
		return false
	}
	state[s.sessionID].PromptEmitted = true
	s.saveStateFile(state)
	return true
}

// AddEvaluation adds a new pending evaluation
func (s *SessionState) AddEvaluation(evaluationID, filePath string) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)

	state[s.sessionID].Evaluations = append(state[s.sessionID].Evaluations, Evaluation{
		EvaluationID: evaluationID,
		FilePath:     filePath,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	})

	s.saveStateFile(state)
}

// GetPendingEvaluations returns pending evaluations for this session
func (s *SessionState) GetPendingEvaluations() []Evaluation {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.Evaluations
	}
	return []Evaluation{}
}

// ClearEvaluations removes all evaluations for this session
func (s *SessionState) ClearEvaluations() {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].Evaluations = []Evaluation{}
	s.saveStateFile(state)
}

// GetRepromptCount returns the current re-prompt count
func (s *SessionState) GetRepromptCount() int {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.RepromptCount
	}
	return 0
}

// IncrementRepromptCount increments and returns the re-prompt count
func (s *SessionState) IncrementRepromptCount() int {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].RepromptCount++
	newCount := state[s.sessionID].RepromptCount
	s.saveStateFile(state)
	return newCount
}

// ResetRepromptCount resets the re-prompt count to 0
func (s *SessionState) ResetRepromptCount() {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].RepromptCount = 0
	s.saveStateFile(state)
}

// CleanupStaleForPlatform removes stale evaluations for the given platform
func CleanupStaleForPlatform(platform string) int {
	stateMutex.Lock()
	defer stateMutex.Unlock()

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
			tmpPath := stateFilePath + ".tmp"
			if err := os.WriteFile(tmpPath, jsonData, 0644); err == nil {
				os.Rename(tmpPath, stateFilePath)
			}
		}
	}

	return removedCount
}
