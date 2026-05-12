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

type PolicyInfo struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Decision    string `json:"decision,omitempty"`
	Enforcement string `json:"enforcement,omitempty"`
	Reason      string `json:"reason,omitempty"`
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
	Policy        *PolicyInfo            `json:"policy,omitempty"`
	Destination   *DestinationInfo       `json:"destination,omitempty"`
	Health        *HealthInfo            `json:"health,omitempty"`
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
	return nil
}
