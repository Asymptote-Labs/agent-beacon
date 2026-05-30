package beaconevent

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptotetrace"
)

const (
	Vendor        = asymptotetrace.Vendor
	Product       = asymptotetrace.Product
	SchemaVersion = asymptotetrace.SchemaVersion
)

type EventInfo = asymptotetrace.EventInfo
type EndpointInfo = asymptotetrace.EndpointInfo
type UserInfo = asymptotetrace.UserInfo
type HarnessInfo = asymptotetrace.HarnessInfo
type SessionInfo = asymptotetrace.SessionInfo
type RunInfo = asymptotetrace.RunInfo
type ToolInfo = asymptotetrace.ToolInfo
type FileInfo = asymptotetrace.FileInfo
type CommandInfo = asymptotetrace.CommandInfo
type MCPInfo = asymptotetrace.MCPInfo
type ApprovalInfo = asymptotetrace.ApprovalInfo
type PolicyInfo = asymptotetrace.PolicyInfo
type PromptInfo = asymptotetrace.PromptInfo
type ContentInfo = asymptotetrace.ContentInfo

type Event struct {
	ObservedAt    time.Time                       `json:"-"`
	Timestamp     string                          `json:"timestamp"`
	Vendor        string                          `json:"vendor"`
	Product       string                          `json:"product"`
	SchemaVersion string                          `json:"schema_version"`
	Event         EventInfo                       `json:"event"`
	Severity      string                          `json:"severity"`
	Endpoint      EndpointInfo                    `json:"endpoint"`
	User          UserInfo                        `json:"user,omitempty"`
	Harness       HarnessInfo                     `json:"harness"`
	Origin        asymptotetrace.Origin           `json:"origin,omitempty"`
	Run           *RunInfo                        `json:"run,omitempty"`
	Session       *SessionInfo                    `json:"session,omitempty"`
	Tool          *ToolInfo                       `json:"tool,omitempty"`
	File          *FileInfo                       `json:"file,omitempty"`
	Command       *CommandInfo                    `json:"command,omitempty"`
	MCP           *MCPInfo                        `json:"mcp,omitempty"`
	Approval      *ApprovalInfo                   `json:"approval,omitempty"`
	Policy        *PolicyInfo                     `json:"policy,omitempty"`
	Prompt        *PromptInfo                     `json:"prompt,omitempty"`
	Content       *ContentInfo                    `json:"content,omitempty"`
	Destination   *asymptotetrace.DestinationInfo `json:"destination,omitempty"`
	Health        *asymptotetrace.HealthInfo      `json:"health,omitempty"`
	Model         string                          `json:"model,omitempty"`
	Repository    string                          `json:"repository,omitempty"`
	Branch        string                          `json:"branch,omitempty"`
	Message       string                          `json:"message,omitempty"`
	Raw           map[string]interface{}          `json:"raw,omitempty"`
	Truncated     bool                            `json:"field_truncated,omitempty"`
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
	base := asymptotetrace.NewEvent(asymptotetrace.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: asymptotetrace.Severity(severity),
		Harness:  HarnessInfo{Name: harnessName},
	})
	return Event{
		ObservedAt:    ts.UTC(),
		Timestamp:     ts.UTC().Format(time.RFC3339),
		Vendor:        base.Vendor,
		Product:       base.Product,
		SchemaVersion: base.SchemaVersion,
		Event:         base.Event,
		Severity:      string(base.Severity),
		Endpoint:      base.Endpoint,
		User:          base.User,
		Harness:       base.Harness,
	}
}
