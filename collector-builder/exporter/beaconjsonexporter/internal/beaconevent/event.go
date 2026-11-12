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
type GenAIContextInfo = asymptoteobserve.GenAIContextInfo
type GenAIUsageInfo = asymptoteobserve.GenAIUsageInfo
type GenAIUsageCacheCreationInfo = asymptoteobserve.GenAIUsageCacheCreationInfo
type GenAIUsageCacheReadInfo = asymptoteobserve.GenAIUsageCacheReadInfo
type GenAIUsageReasoningInfo = asymptoteobserve.GenAIUsageReasoningInfo
type GenAIWorkflowInfo = asymptoteobserve.GenAIWorkflowInfo
type UserAgentInfo = asymptoteobserve.UserAgentInfo

// Base is the event as it is written: asymptoteobserve.Event, the type every Beacon writer shares.
// Event embeds it under this name rather than as asymptoteobserve.Event so that e.Event stays the
// event block (action, category, ...): embedded directly, the field would itself be named Event and
// hide it.
type Base = asymptoteobserve.Event

// Event is an exporter event: the written event, plus what the exporter tracks about it in memory
// and never writes.
type Event struct {
	Base
	ObservedAt time.Time `json:"-"`

	// codexConversation is set on a Codex app-server record whose conversation no originator
	// has named yet; CodexSurfaces.Sort holds it until one does. It is never written.
	codexConversation string
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
		// Every event this exporter produces came off the runtime as OpenTelemetry, whatever
		// signal it arrived on, so the method is fixed here at the single construction point
		// rather than repeated at each of the log/span/metric call sites.
		Harness: HarnessInfo{Name: harnessName, CollectionMethod: asymptoteobserve.CollectionMethodOTLP},
	})
	base.Timestamp = asymptoteobserve.FormatTimestamp(ts)
	return Event{Base: base, ObservedAt: ts.UTC()}
}
