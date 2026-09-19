// Package claudesession reads Claude Code's local session transcripts and turns them into
// Beacon endpoint telemetry.
//
// This is an after-the-fact collection path. Claude has already committed the JSONL row before
// Beacon sees it, so every mapped event carries harness.collection_method=poll. That is
// deliberately different from the Claude hook adapter, which observes hooks as they fire and can
// participate in the optional policy seam.
package claudesession

import (
	"encoding/json"
	"fmt"
)

const Harness = "claude_code"

type Entry struct {
	ParentUUID       string                 `json:"parentUuid,omitempty"`
	IsSidechain      bool                   `json:"isSidechain,omitempty"`
	PromptID         string                 `json:"promptId,omitempty"`
	Type             string                 `json:"type"`
	Subtype          string                 `json:"subtype,omitempty"`
	UUID             string                 `json:"uuid,omitempty"`
	Timestamp        string                 `json:"timestamp,omitempty"`
	UserType         string                 `json:"userType,omitempty"`
	Entrypoint       string                 `json:"entrypoint,omitempty"`
	CWD              string                 `json:"cwd,omitempty"`
	SessionID        string                 `json:"sessionId,omitempty"`
	Version          string                 `json:"version,omitempty"`
	GitBranch        string                 `json:"gitBranch,omitempty"`
	IsMeta           bool                   `json:"isMeta,omitempty"`
	Message          map[string]interface{} `json:"message,omitempty"`
	Attachment       map[string]interface{} `json:"attachment,omitempty"`
	RequestID        string                 `json:"requestId,omitempty"`
	AttributionAgent string                 `json:"attributionAgent,omitempty"`
	AgentID          string                 `json:"agentId,omitempty"`
}

type Record struct {
	Line  int
	Entry Entry
}

type SubagentMeta struct {
	AgentType   string `json:"agentType,omitempty"`
	Description string `json:"description,omitempty"`
	ToolUseID   string `json:"toolUseId,omitempty"`
}

type SessionIndex struct {
	Version      int          `json:"version"`
	Entries      []IndexEntry `json:"entries"`
	OriginalPath string       `json:"originalPath"`
}

type IndexEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	FileMtime    int64  `json:"fileMtime"`
	FirstPrompt  string `json:"firstPrompt"`
	Summary      string `json:"summary"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	GitBranch    string `json:"gitBranch"`
	ProjectPath  string `json:"projectPath"`
	IsSidechain  bool   `json:"isSidechain"`
}

func decodeEntry(line []byte) (*Entry, error) {
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, fmt.Errorf("claude session entry: %w", err)
	}
	if entry.Type == "" {
		return nil, fmt.Errorf("claude session entry: missing type")
	}
	return &entry, nil
}
