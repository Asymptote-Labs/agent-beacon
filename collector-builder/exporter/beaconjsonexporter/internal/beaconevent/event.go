package beaconevent

import (
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

const (
	Vendor        = asymptoteobserve.Vendor
	Product       = asymptoteobserve.Product
	SchemaVersion = asymptoteobserve.SchemaVersion
)

type EventInfo = asymptoteobserve.EventInfo
type EndpointInfo = asymptoteobserve.EndpointInfo
type UserInfo = asymptoteobserve.UserInfo
type HarnessInfo = asymptoteobserve.HarnessInfo
type SessionInfo = asymptoteobserve.SessionInfo
type TraceInfo = asymptoteobserve.TraceInfo
type ErrorInfo = asymptoteobserve.ErrorInfo
type JSONRPCInfo = asymptoteobserve.JSONRPCInfo
type JSONRPCRequestInfo = asymptoteobserve.JSONRPCRequestInfo
type JSONRPCProtocolInfo = asymptoteobserve.JSONRPCProtocolInfo
type NetworkInfo = asymptoteobserve.NetworkInfo
type NetworkProtocolInfo = asymptoteobserve.NetworkProtocolInfo
type RPCInfo = asymptoteobserve.RPCInfo
type RPCResponseInfo = asymptoteobserve.RPCResponseInfo
type RunInfo = asymptoteobserve.RunInfo
type ToolInfo = asymptoteobserve.ToolInfo
type FileInfo = asymptoteobserve.FileInfo
type CommandInfo = asymptoteobserve.CommandInfo
type MCPInfo = asymptoteobserve.MCPInfo
type MCPMethodInfo = asymptoteobserve.MCPMethodInfo
type MCPProtocolInfo = asymptoteobserve.MCPProtocolInfo
type MCPResourceInfo = asymptoteobserve.MCPResourceInfo
type MCPSessionInfo = asymptoteobserve.MCPSessionInfo
type ApprovalInfo = asymptoteobserve.ApprovalInfo
type PolicyInfo = asymptoteobserve.PolicyInfo
type PromptInfo = asymptoteobserve.PromptInfo
type ContentInfo = asymptoteobserve.ContentInfo
type ServerInfo = asymptoteobserve.ServerInfo
type GenAIInfo = asymptoteobserve.GenAIInfo
type GenAIAgentInfo = asymptoteobserve.GenAIAgentInfo
type GenAIConversationInfo = asymptoteobserve.GenAIConversationInfo
type GenAIDataSourceInfo = asymptoteobserve.GenAIDataSourceInfo
type GenAIEmbeddingsInfo = asymptoteobserve.GenAIEmbeddingsInfo
type GenAIEvaluationInfo = asymptoteobserve.GenAIEvaluationInfo
type GenAIEvaluationScoreInfo = asymptoteobserve.GenAIEvaluationScoreInfo
type GenAIInputInfo = asymptoteobserve.GenAIInputInfo
type GenAIOperationInfo = asymptoteobserve.GenAIOperationInfo
type GenAIOutputInfo = asymptoteobserve.GenAIOutputInfo
type GenAIPromptInfo = asymptoteobserve.GenAIPromptInfo
type GenAIProviderInfo = asymptoteobserve.GenAIProviderInfo
type GenAIRequestInfo = asymptoteobserve.GenAIRequestInfo
type GenAIResponseInfo = asymptoteobserve.GenAIResponseInfo
type GenAIRetrievalInfo = asymptoteobserve.GenAIRetrievalInfo
type GenAITokenInfo = asymptoteobserve.GenAITokenInfo
type GenAIToolInfo = asymptoteobserve.GenAIToolInfo
type GenAIToolCallInfo = asymptoteobserve.GenAIToolCallInfo
type GenAIUsageInfo = asymptoteobserve.GenAIUsageInfo
type GenAIUsageCacheCreationInfo = asymptoteobserve.GenAIUsageCacheCreationInfo
type GenAIUsageCacheReadInfo = asymptoteobserve.GenAIUsageCacheReadInfo
type GenAIUsageReasoningInfo = asymptoteobserve.GenAIUsageReasoningInfo
type GenAIWorkflowInfo = asymptoteobserve.GenAIWorkflowInfo

type Event struct {
	ObservedAt    time.Time                         `json:"-"`
	Timestamp     string                            `json:"timestamp"`
	Vendor        string                            `json:"vendor"`
	Product       string                            `json:"product"`
	SchemaVersion string                            `json:"schema_version"`
	Event         EventInfo                         `json:"event"`
	Severity      string                            `json:"severity"`
	Endpoint      EndpointInfo                      `json:"endpoint"`
	User          UserInfo                          `json:"user,omitempty"`
	Harness       HarnessInfo                       `json:"harness"`
	Origin        asymptoteobserve.Origin           `json:"origin,omitempty"`
	Error         *ErrorInfo                        `json:"error,omitempty"`
	Run           *RunInfo                          `json:"run,omitempty"`
	Session       *SessionInfo                      `json:"session,omitempty"`
	Trace         *TraceInfo                        `json:"trace,omitempty"`
	JSONRPC       *JSONRPCInfo                      `json:"jsonrpc,omitempty"`
	Network       *NetworkInfo                      `json:"network,omitempty"`
	RPC           *RPCInfo                          `json:"rpc,omitempty"`
	Server        *ServerInfo                       `json:"server,omitempty"`
	Tool          *ToolInfo                         `json:"tool,omitempty"`
	File          *FileInfo                         `json:"file,omitempty"`
	Command       *CommandInfo                      `json:"command,omitempty"`
	MCP           *MCPInfo                          `json:"mcp,omitempty"`
	Approval      *ApprovalInfo                     `json:"approval,omitempty"`
	Policy        *PolicyInfo                       `json:"policy,omitempty"`
	Prompt        *PromptInfo                       `json:"prompt,omitempty"`
	Content       *ContentInfo                      `json:"content,omitempty"`
	Destination   *asymptoteobserve.DestinationInfo `json:"destination,omitempty"`
	Health        *asymptoteobserve.HealthInfo      `json:"health,omitempty"`
	GenAI         *GenAIInfo                        `json:"gen_ai,omitempty"`
	Model         string                            `json:"model,omitempty"`
	Repository    string                            `json:"repository,omitempty"`
	Branch        string                            `json:"branch,omitempty"`
	Message       string                            `json:"message,omitempty"`
	Raw           map[string]interface{}            `json:"raw,omitempty"`
	Truncated     bool                              `json:"field_truncated,omitempty"`
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
	base := asymptoteobserve.NewEvent(asymptoteobserve.NewEventOptions{
		Action:   action,
		Category: category,
		Severity: asymptoteobserve.Severity(severity),
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
