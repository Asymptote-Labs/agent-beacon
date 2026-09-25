// Package openclawsession reads OpenClaw Gateway's committed session store and
// turns it into Beacon endpoint telemetry.
//
// This is deliberately separate from Beacon's managed OpenClaw plugin. The
// plugin observes live hooks; this package is a poll collector for historical
// or missed sessions under:
//
//	~/.openclaw/agents/<profile>/sessions/
//
// Each event it emits is marked harness.collection_method=poll because Beacon
// reads records OpenClaw already wrote rather than participating in the tool
// call.
package openclawsession

import "encoding/json"

const Harness = "openclaw_gateway"

type TraceRef struct {
	ID              string
	Profile         string
	SourcePath      string
	Title           string
	Preview         string
	Directory       string
	UpdatedAtUnixMS int64
	SizeBytes       int64
	// SessionKey is the Gateway session key the sessions.json index files the transcript under,
	// such as "agent:main:main". Empty for a transcript found outside the index.
	SessionKey string
}

type SessionIndexEntry struct {
	SessionID          string `json:"sessionId"`
	SessionFile        string `json:"sessionFile"`
	UpdatedAt          any    `json:"updatedAt"`
	WorkspaceDir       string `json:"workspaceDir"`
	SystemPromptReport any    `json:"systemPromptReport"`
}

type RawEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Timestamp  any             `json:"timestamp"`
	CWD        string          `json:"cwd"`
	ModelID    string          `json:"modelId"`
	Model      string          `json:"model"`
	CustomType string          `json:"customType"`
	Content    any             `json:"content"`
	Data       json.RawMessage `json:"data"`
	Message    *Message        `json:"message"`
	Error      any             `json:"error"`
	Raw        map[string]any  `json:"-"`
}

type Message struct {
	Role      string `json:"role"`
	Timestamp any    `json:"timestamp"`
	ModelID   string `json:"modelId"`
	Model     string `json:"model"`
	Content   any    `json:"content"`
	Usage     *Usage `json:"usage"`
}

type ContentBlock struct {
	Type        string         `json:"type"`
	Text        string         `json:"text"`
	Content     any            `json:"content"`
	Name        string         `json:"name"`
	Tool        string         `json:"tool"`
	ToolName    string         `json:"toolName"`
	ToolNameAlt string         `json:"tool_name"`
	ID          string         `json:"id"`
	CallID      string         `json:"callId"`
	CallIDAlt   string         `json:"call_id"`
	Input       any            `json:"input"`
	Args        any            `json:"args"`
	Arguments   any            `json:"arguments"`
	Output      any            `json:"output"`
	Result      any            `json:"result"`
	Status      string         `json:"status"`
	Error       any            `json:"error"`
	Raw         map[string]any `json:"-"`
}

type Usage struct {
	Input           int64 `json:"input"`
	Output          int64 `json:"output"`
	CacheRead       int64 `json:"cacheRead"`
	CacheWrite      int64 `json:"cacheWrite"`
	Reasoning       int64 `json:"reasoning"`
	ReasoningTokens int64 `json:"reasoningTokens"`
}

type Record struct {
	Order       int
	NativeID    string
	TimestampMS int64
	Type        string
	Role        string
	Content     string
	ModelID     string
	ToolName    string
	CallID      string
	Args        map[string]any
	Output      any
	Status      string
	Tokens      *Usage
	Raw         map[string]any
}
