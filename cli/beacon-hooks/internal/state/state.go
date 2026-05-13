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
	CreatedAt     string       `json:"created_at,omitempty"`

	// Secure-by-Design fields (used by Cursor's pre-tool gate)
	SbdPolicies     string `json:"sbd_policies,omitempty"`
	SbdGenerationID string `json:"sbd_generation_id,omitempty"`
	SbdInjected     bool   `json:"sbd_injected,omitempty"`
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

// SetSbdPolicies stores local policy context and resets the injection flag for a new generation.
func (s *SessionState) SetSbdPolicies(policies, generationID string) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].SbdPolicies = policies
	state[s.sessionID].SbdGenerationID = generationID
	state[s.sessionID].SbdInjected = false
	s.saveStateFile(state)
}

// GetSbdState returns the current Secure-by-Design state for this session.
func (s *SessionState) GetSbdState() (policies, generationID string, injected bool) {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		return sessionData.SbdPolicies, sessionData.SbdGenerationID, sessionData.SbdInjected
	}
	return "", "", false
}

// ClearSbdPolicies removes cached SbD policies for this session.
// Called on Cursor early-return paths in prompt-submit to prevent stale policies
// from being injected in later generations.
func (s *SessionState) ClearSbdPolicies() {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	if sessionData, ok := state[s.sessionID]; ok {
		sessionData.SbdPolicies = ""
		sessionData.SbdGenerationID = ""
		sessionData.SbdInjected = false
		s.saveStateFile(state)
	}
}

// MarkSbdInjected sets the injection flag to true, indicating policies have been
// delivered to the agent for the current generation.
func (s *SessionState) MarkSbdInjected() {
	stateMutex.Lock()
	defer stateMutex.Unlock()

	state := s.loadStateFile()
	s.ensureSession(state)
	state[s.sessionID].SbdInjected = true
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
