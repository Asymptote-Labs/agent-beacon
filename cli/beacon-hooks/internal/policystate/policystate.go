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

const maxEntries = 50

// Entry is one recorded decision. Target is already masked.
type Entry struct {
	At         time.Time `json:"at"`
	Decision   string    `json:"decision"`
	Tool       string    `json:"tool"`
	Target     string    `json:"target"`
	Reason     string    `json:"reason"`
	ToolUseID  string    `json:"tool_use_id,omitempty"`
	PolicyID   string    `json:"policy_id,omitempty"`
	PolicyName string    `json:"policy_name,omitempty"`
	FindingID  string    `json:"finding_id,omitempty"`
	// Outcome is the developer's answer to an ask: approved, rejected or
	// dismissed. Empty while the ask is pending.
	Outcome    string    `json:"outcome,omitempty"`
	Comment    string    `json:"comment,omitempty"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	// Prompt blocks: the blocked secrets' fingerprints, when the developer
	// allowed them with /beacon-allow, and when the resent prompt used that.
	Fingerprints []string  `json:"fingerprints,omitempty"`
	AllowedAt    time.Time `json:"allowed_at,omitempty"`
	UsedAt       time.Time `json:"used_at,omitempty"`
}

// PromptBlock is the Decision and Tool of a blocked prompt's entry.
const (
	DecisionBlock = "block"
	ToolPrompt    = "prompt"
)

// LatestPromptBlock returns the most recent prompt block in the session that
// happened within window of now and has not been allowed yet.
func LatestPromptBlock(sessionID string, now time.Time, window time.Duration) (Entry, bool) {
	entries := Load(sessionID)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Decision != DecisionBlock || e.Tool != ToolPrompt {
			continue
		}
		if now.Sub(e.At) > window || !e.AllowedAt.IsZero() {
			return Entry{}, false
		}
		return e, true
	}
	return Entry{}, false
}

// GrantPromptOverride marks the prompt block with this id as allowed once.
func GrantPromptOverride(sessionID, id, reason string, now time.Time) error {
	entries := Load(sessionID)
	for i := range entries {
		if entries[i].ToolUseID == id && entries[i].Decision == DecisionBlock {
			entries[i].AllowedAt, entries[i].Outcome, entries[i].Comment = now, "approved", reason
		}
	}
	return save(sessionID, entries)
}

// ConsumePromptOverride finds an allowed, unused prompt block from within
// window of now (measured from the block, the same clock /beacon-allow uses)
// whose fingerprints cover every secret in the resent prompt, marks it used,
// and returns it. A prompt carrying any other secret does not qualify.
func ConsumePromptOverride(sessionID string, fingerprints []string, now time.Time, window time.Duration) (Entry, bool) {
	entries := Load(sessionID)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Decision != DecisionBlock || e.Tool != ToolPrompt || e.AllowedAt.IsZero() || !e.UsedAt.IsZero() {
			continue
		}
		if now.Sub(e.At) > window || !covers(e.Fingerprints, fingerprints) {
			continue
		}
		entries[i].UsedAt = now
		if save(sessionID, entries) != nil {
			return Entry{}, false
		}
		return entries[i], true
	}
	return Entry{}, false
}

func covers(allowed, wanted []string) bool {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	for _, w := range wanted {
		if !set[w] {
			return false
		}
	}
	return len(wanted) > 0
}

// Pending returns the session's asks that have no recorded answer yet.
func Pending(sessionID string) []Entry {
	var out []Entry
	for _, e := range Load(sessionID) {
		if e.Decision == "ask" && e.Outcome == "" && e.ToolUseID != "" {
			out = append(out, e)
		}
	}
	return out
}

// Answer records the developer's answer on the ask with this tool_use_id.
func Answer(sessionID, toolUseID, outcome, comment string, at time.Time) error {
	entries := Load(sessionID)
	for i := range entries {
		if entries[i].ToolUseID == toolUseID && entries[i].Outcome == "" {
			entries[i].Outcome, entries[i].Comment, entries[i].AnsweredAt = outcome, comment, at
		}
	}
	return save(sessionID, entries)
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
	return save(sessionID, entries)
}

func save(sessionID string, entries []Entry) error {
	p := path(sessionID)
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
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
