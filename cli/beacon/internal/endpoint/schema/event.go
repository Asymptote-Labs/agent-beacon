package schema

import (
	"errors"
	"os"
	"runtime"
	"time"
)

const (
	Vendor        = "beacon"
	Product       = "endpoint-agent"
	SchemaVersion = "1.0"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type EventInfo struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Category string `json:"category,omitempty"`
}

type EndpointInfo struct {
	Hostname     string `json:"hostname,omitempty"`
	OS           string `json:"os"`
	AgentVersion string `json:"agent_version,omitempty"`
}

type UserInfo struct {
	Name string `json:"name,omitempty"`
	UID  string `json:"uid,omitempty"`
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

type PolicyInfo struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Decision    string `json:"decision,omitempty"`
	Enforcement string `json:"enforcement,omitempty"`
	Reason      string `json:"reason,omitempty"`
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

const (
	ContentRetentionMetadata = "metadata"
	ContentRetentionRedacted = "redacted"
	ContentRetentionFull     = "full"
)

// TokenUsage holds token consumption counts for a single event.
// Fields that are zero are omitted from JSON to keep non-token events lean.
type TokenUsage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

// Total returns the sum of all token type counts.
func (t *TokenUsage) Total() int64 {
	if t == nil {
		return 0
	}
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// IsZero reports whether no token counts have been recorded.
func (t *TokenUsage) IsZero() bool {
	return t == nil || (t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0)
}

type DestinationInfo struct {
	Type   string `json:"type,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Status string `json:"status,omitempty"`
}

type HealthInfo struct {
	Component string `json:"component,omitempty"`
	Status    string `json:"status,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type Event struct {
	Timestamp     string                 `json:"timestamp"`
	Vendor        string                 `json:"vendor"`
	Product       string                 `json:"product"`
	SchemaVersion string                 `json:"schema_version"`
	Event         EventInfo              `json:"event"`
	Severity      Severity               `json:"severity"`
	Endpoint      EndpointInfo           `json:"endpoint"`
	User          UserInfo               `json:"user,omitempty"`
	Harness       HarnessInfo            `json:"harness"`
	Session       *SessionInfo           `json:"session,omitempty"`
	Tool          *ToolInfo              `json:"tool,omitempty"`
	File          *FileInfo              `json:"file,omitempty"`
	Command       *CommandInfo           `json:"command,omitempty"`
	MCP           *MCPInfo               `json:"mcp,omitempty"`
	Approval      *ApprovalInfo          `json:"approval,omitempty"`
	Policy        *PolicyInfo            `json:"policy,omitempty"`
	Prompt        *PromptInfo            `json:"prompt,omitempty"`
	Content       *ContentInfo           `json:"content,omitempty"`
	Destination   *DestinationInfo       `json:"destination,omitempty"`
	Health        *HealthInfo            `json:"health,omitempty"`
	Tokens        *TokenUsage            `json:"tokens,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Repository    string                 `json:"repository,omitempty"`
	Branch        string                 `json:"branch,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Raw           map[string]interface{} `json:"raw,omitempty"`
	Truncated     bool                   `json:"field_truncated,omitempty"`
}

type NewEventOptions struct {
	Action       string
	Category     string
	Severity     Severity
	Harness      HarnessInfo
	AgentVersion string
	Message      string
}

func NewEvent(opts NewEventOptions) Event {
	hostname, _ := os.Hostname()
	userName := os.Getenv("USER")
	if userName == "" {
		userName = os.Getenv("USERNAME")
	}
	severity := opts.Severity
	if severity == "" {
		severity = SeverityInfo
	}
	return Event{
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Vendor:        Vendor,
		Product:       Product,
		SchemaVersion: SchemaVersion,
		Event: EventInfo{
			Kind:     "agent_runtime",
			Action:   opts.Action,
			Category: opts.Category,
		},
		Severity: severity,
		Endpoint: EndpointInfo{
			Hostname:     hostname,
			OS:           runtime.GOOS,
			AgentVersion: opts.AgentVersion,
		},
		User: UserInfo{
			Name: userName,
			UID:  os.Getenv("UID"),
		},
		Harness: opts.Harness,
		Message: opts.Message,
	}
}

func (e Event) Validate() error {
	if e.Vendor != Vendor {
		return errors.New("vendor must be beacon")
	}
	if e.Product != Product {
		return errors.New("product must be endpoint-agent")
	}
	if e.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if e.Event.Kind == "" || e.Event.Action == "" {
		return errors.New("event.kind and event.action are required")
	}
	if e.Severity == "" {
		return errors.New("severity is required")
	}
	if e.Endpoint.OS == "" {
		return errors.New("endpoint.os is required")
	}
	if e.Harness.Name == "" {
		return errors.New("harness.name is required")
	}
	if e.Content != nil {
		switch e.Content.Retention {
		case ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull:
		default:
			return errors.New("content.retention must be metadata, redacted, or full")
		}
	}
	return nil
}
