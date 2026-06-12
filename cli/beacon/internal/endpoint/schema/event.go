package schema

import "github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"

const (
	Vendor        = asymptoteobserve.Vendor
	Product       = asymptoteobserve.Product
	SchemaVersion = asymptoteobserve.SchemaVersion
)

type Severity = asymptoteobserve.Severity

const (
	SeverityInfo     = asymptoteobserve.SeverityInfo
	SeverityLow      = asymptoteobserve.SeverityLow
	SeverityMedium   = asymptoteobserve.SeverityMedium
	SeverityHigh     = asymptoteobserve.SeverityHigh
	SeverityCritical = asymptoteobserve.SeverityCritical
)

type Origin = asymptoteobserve.Origin

const (
	OriginLocal = asymptoteobserve.OriginLocal
	OriginCloud = asymptoteobserve.OriginCloud
	OriginCI    = asymptoteobserve.OriginCI
)

const (
	AttributeOrigin        = asymptoteobserve.AttributeOrigin
	AttributeRunProvider   = asymptoteobserve.AttributeRunProvider
	AttributeRunID         = asymptoteobserve.AttributeRunID
	AttributeRunAttempt    = asymptoteobserve.AttributeRunAttempt
	AttributeRunWorkflow   = asymptoteobserve.AttributeRunWorkflow
	AttributeRunJob        = asymptoteobserve.AttributeRunJob
	AttributeRunEventName  = asymptoteobserve.AttributeRunEventName
	AttributeRunCommit     = asymptoteobserve.AttributeRunCommit
	AttributeRunRepository = asymptoteobserve.AttributeRunRepository
	AttributeRunBranch     = asymptoteobserve.AttributeRunBranch
	AttributeRunPR         = asymptoteobserve.AttributeRunPR
	AttributeRunPRNumber   = asymptoteobserve.AttributeRunPRNumber
	AttributeRunActor      = asymptoteobserve.AttributeRunActor
	AttributeRunEphemeral  = asymptoteobserve.AttributeRunEphemeral
)

type EventInfo = asymptoteobserve.EventInfo
type EndpointInfo = asymptoteobserve.EndpointInfo
type UserInfo = asymptoteobserve.UserInfo
type HarnessInfo = asymptoteobserve.HarnessInfo
type SessionInfo = asymptoteobserve.SessionInfo
type TraceInfo = asymptoteobserve.TraceInfo
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
type DestinationInfo = asymptoteobserve.DestinationInfo
type HealthInfo = asymptoteobserve.HealthInfo
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
type Event = asymptoteobserve.Event
type NewEventOptions = asymptoteobserve.NewEventOptions
type Envelope = asymptoteobserve.Envelope

const (
	ContentRetentionMetadata = asymptoteobserve.ContentRetentionMetadata
	ContentRetentionRedacted = asymptoteobserve.ContentRetentionRedacted
	ContentRetentionFull     = asymptoteobserve.ContentRetentionFull
)

var NewEvent = asymptoteobserve.NewEvent
