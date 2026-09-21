package copilotsession

import (
	"encoding/json"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

const (
	Harness       = "copilot_cli"
	StateVersion  = 1
	MaxLineBytes  = 8 * 1024 * 1024
	EventsFile    = "events.jsonl"
	WorkspaceFile = "workspace.yaml"
)

type SessionRef struct {
	ID            string
	Path          string
	Dir           string
	CopilotDir    string
	ModTimeUnixMS int64
	SizeBytes     int64
	Meta          *WorkspaceMeta
}

type WorkspaceMeta struct {
	ID           string `yaml:"id"`
	CWD          string `yaml:"cwd"`
	GitRoot      string `yaml:"git_root"`
	Repository   string `yaml:"repository"`
	Branch       string `yaml:"branch"`
	Name         string `yaml:"name"`
	CreatedAt    string `yaml:"created_at"`
	UpdatedAt    string `yaml:"updated_at"`
	SummaryCount int    `yaml:"summary_count"`
}

type Record struct {
	Line     int
	ID       string                 `json:"id"`
	ParentID string                 `json:"parentId,omitempty"`
	Time     interface{}            `json:"timestamp,omitempty"`
	Type     string                 `json:"type"`
	Data     map[string]interface{} `json:"data,omitempty"`
	Raw      json.RawMessage        `json:"-"`
}

type Stats struct {
	Lines       int
	Decoded     int
	Malformed   int
	PartialTail bool
	FirstError  error
}

type MappedEvent struct {
	DedupID    string
	SourceLine int
	Event      schema.Event
}

type MapOptions struct {
	MinLine            int
	SkipSessionStarted bool
}
