package asymptotetrace

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

type Origin string

const (
	OriginLocal Origin = "local"
	OriginCloud Origin = "cloud"
	OriginCI    Origin = "ci"
)

const (
	AttributeOrigin        = "beacon.origin"
	AttributeRunProvider   = "beacon.run.provider"
	AttributeRunID         = "beacon.run.run_id"
	AttributeRunAttempt    = "beacon.run.run_attempt"
	AttributeRunWorkflow   = "beacon.run.workflow"
	AttributeRunJob        = "beacon.run.job"
	AttributeRunEventName  = "beacon.run.event_name"
	AttributeRunCommit     = "beacon.run.commit"
	AttributeRunRepository = "beacon.run.repository"
	AttributeRunBranch     = "beacon.run.branch"
	AttributeRunPR         = "beacon.run.pr"
	AttributeRunPRNumber   = "beacon.run.pr_number"
	AttributeRunActor      = "beacon.run.actor"
	AttributeRunEphemeral  = "beacon.run.ephemeral"
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

type RunInfo struct {
	Provider   string `json:"provider,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	RunAttempt string `json:"run_attempt,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	Job        string `json:"job,omitempty"`
	EventName  string `json:"event_name,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	// PR holds the raw pull-request ref (e.g. refs/pull/12/merge) and is only
	// populated on pull-request events; it is left empty for non-PR refs such
	// as push builds. PRNumber is the parsed pull-request number.
	PR        string `json:"pr,omitempty"`
	PRNumber  string `json:"pr_number,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Ephemeral bool   `json:"ephemeral,omitempty"`
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
	Origin        Origin                 `json:"origin,omitempty"`
	Run           *RunInfo               `json:"run,omitempty"`
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
	Origin       Origin
	Run          *RunInfo
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
		Origin:  opts.Origin,
		Run:     opts.Run,
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
	if e.Origin != "" {
		switch e.Origin {
		case OriginLocal, OriginCloud, OriginCI:
		default:
			return errors.New("origin must be local, cloud, or ci")
		}
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
