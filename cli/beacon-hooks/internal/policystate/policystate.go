// Package policystate keeps the policy hook's per-session memory: the decisions
// it has already made, so the judge can see an earlier refusal when the agent
// tries the same secret another way.
package policystate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const maxEntries = 10

// Entry is one recorded decision. Target is already masked.
type Entry struct {
	At       time.Time `json:"at"`
	Decision string    `json:"decision"`
	Tool     string    `json:"tool"`
	Target   string    `json:"target"`
	Reason   string    `json:"reason"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Dir is ~/.beacon/endpoint/policy/sessions unless overridden for tests.
func Dir() string {
	if d := os.Getenv("BEACON_POLICY_STATE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".beacon", "endpoint", "policy", "sessions")
}

func path(sessionID string) string {
	dir := Dir()
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, unsafeChars.ReplaceAllString(sessionID, "_")+".json")
}

// Load returns the session's recorded decisions, oldest first.
func Load(sessionID string) []Entry {
	p := path(sessionID)
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var entries []Entry
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	return entries
}

// Append records a decision, keeping the most recent maxEntries.
func Append(sessionID string, e Entry) error {
	p := path(sessionID)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	entries := append(Load(sessionID), e)
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
