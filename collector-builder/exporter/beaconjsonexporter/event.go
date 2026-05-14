package beaconjsonexporter

import (
	"os"
	"runtime"
	"time"
)

const (
	vendor        = "beacon"
	product       = "endpoint-agent"
	schemaVersion = "1.0"
)

type eventInfo struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Category string `json:"category,omitempty"`
}

type endpointInfo struct {
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os"`
}

type userInfo struct {
	Name string `json:"name,omitempty"`
}

type harnessInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	ConfigPath     string `json:"config_path,omitempty"`
}

type sessionInfo struct {
	ID               string `json:"id,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type toolInfo struct {
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
}

type fileInfo struct {
	Path      string `json:"path,omitempty"`
	Operation string `json:"operation,omitempty"`
	Language  string `json:"language,omitempty"`
	DiffHash  string `json:"diff_hash,omitempty"`
	DiffBytes int    `json:"diff_bytes,omitempty"`
}

type commandInfo struct {
	Command    string `json:"command,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type mcpInfo struct {
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

type approvalInfo struct {
	Required bool   `json:"required,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type promptInfo struct {
	Text string `json:"text,omitempty"`
}

type contentInfo struct {
	Retention string `json:"retention"`
	Included  bool   `json:"included"`
	Redacted  bool   `json:"redacted,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type beaconEvent struct {
	Timestamp     string                 `json:"timestamp"`
	Vendor        string                 `json:"vendor"`
	Product       string                 `json:"product"`
	SchemaVersion string                 `json:"schema_version"`
	Event         eventInfo              `json:"event"`
	Severity      string                 `json:"severity"`
	Endpoint      endpointInfo           `json:"endpoint"`
	User          userInfo               `json:"user,omitempty"`
	Harness       harnessInfo            `json:"harness"`
	Session       *sessionInfo           `json:"session,omitempty"`
	Tool          *toolInfo              `json:"tool,omitempty"`
	File          *fileInfo              `json:"file,omitempty"`
	Command       *commandInfo           `json:"command,omitempty"`
	MCP           *mcpInfo               `json:"mcp,omitempty"`
	Approval      *approvalInfo          `json:"approval,omitempty"`
	Prompt        *promptInfo            `json:"prompt,omitempty"`
	Content       *contentInfo           `json:"content,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Repository    string                 `json:"repository,omitempty"`
	Branch        string                 `json:"branch,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Raw           map[string]interface{} `json:"raw,omitempty"`
	Truncated     bool                   `json:"field_truncated,omitempty"`
}

func newBeaconEvent(action, category, severity, harnessName string, ts time.Time) beaconEvent {
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
	return beaconEvent{
		Timestamp:     ts.UTC().Format(time.RFC3339),
		Vendor:        vendor,
		Product:       product,
		SchemaVersion: schemaVersion,
		Event: eventInfo{
			Kind:     "agent_runtime",
			Action:   action,
			Category: category,
		},
		Severity: severity,
		Endpoint: endpointInfo{
			Hostname: hostname,
			OS:       runtime.GOOS,
		},
		User: userInfo{
			Name: os.Getenv("USER"),
		},
		Harness: harnessInfo{Name: harnessName},
	}
}
