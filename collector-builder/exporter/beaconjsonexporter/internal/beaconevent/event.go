package beaconevent

import (
	"os"
	"runtime"
	"time"
)

const (
	Vendor        = "beacon"
	Product       = "endpoint-agent"
	SchemaVersion = "1.0"
)

type EventInfo struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Category string `json:"category,omitempty"`
}

type EndpointInfo struct {
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os"`
}

type UserInfo struct {
	Name string `json:"name,omitempty"`
}

type HarnessInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	ConfigPath     string `json:"config_path,omitempty"`
}

type SessionInfo struct {
	ID               string `json:"id,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type ToolInfo struct {
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
}

type FileInfo struct {
	Path      string `json:"path,omitempty"`
	Operation string `json:"operation,omitempty"`
	Language  string `json:"language,omitempty"`
	DiffHash  string `json:"diff_hash,omitempty"`
	DiffBytes int    `json:"diff_bytes,omitempty"`
}

type CommandInfo struct {
	Command    string `json:"command,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type MCPInfo struct {
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

type ApprovalInfo struct {
	Required bool   `json:"required,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PromptInfo struct {
	Text string `json:"text,omitempty"`
}

type ContentInfo struct {
	Retention string `json:"retention"`
	Included  bool   `json:"included"`
	Redacted  bool   `json:"redacted,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Event struct {
	ObservedAt    time.Time              `json:"-"`
	Timestamp     string                 `json:"timestamp"`
	Vendor        string                 `json:"vendor"`
	Product       string                 `json:"product"`
	SchemaVersion string                 `json:"schema_version"`
	Event         EventInfo              `json:"event"`
	Severity      string                 `json:"severity"`
	Endpoint      EndpointInfo           `json:"endpoint"`
	User          UserInfo               `json:"user,omitempty"`
	Harness       HarnessInfo            `json:"harness"`
	Session       *SessionInfo           `json:"session,omitempty"`
	Tool          *ToolInfo              `json:"tool,omitempty"`
	File          *FileInfo              `json:"file,omitempty"`
	Command       *CommandInfo           `json:"command,omitempty"`
	MCP           *MCPInfo               `json:"mcp,omitempty"`
	Approval      *ApprovalInfo          `json:"approval,omitempty"`
	Prompt        *PromptInfo            `json:"prompt,omitempty"`
	Content       *ContentInfo           `json:"content,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Repository    string                 `json:"repository,omitempty"`
	Branch        string                 `json:"branch,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Raw           map[string]interface{} `json:"raw,omitempty"`
	Truncated     bool                   `json:"field_truncated,omitempty"`
}

func NewEvent(action, category, severity, harnessName string, ts time.Time) Event {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if severity == "" {
		severity = "info"
	}
	if harnessName == "" {
		harnessName = "unknown"
	}
	hostname, _ := os.Hostname()
	return Event{
		ObservedAt:    ts.UTC(),
		Timestamp:     ts.UTC().Format(time.RFC3339),
		Vendor:        Vendor,
		Product:       Product,
		SchemaVersion: SchemaVersion,
		Event: EventInfo{
			Kind:     "agent_runtime",
			Action:   action,
			Category: category,
		},
		Severity: severity,
		Endpoint: EndpointInfo{
			Hostname: hostname,
			OS:       runtime.GOOS,
		},
		User:    UserInfo{Name: os.Getenv("USER")},
		Harness: HarnessInfo{Name: harnessName},
	}
}
