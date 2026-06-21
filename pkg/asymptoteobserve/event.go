package asymptoteobserve

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

type TraceInfo struct {
	ID           string `json:"id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
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

type MCPMethodInfo struct {
	Name string `json:"name,omitempty"`
}

type MCPProtocolInfo struct {
	Version string `json:"version,omitempty"`
}

type MCPResourceInfo struct {
	URI string `json:"uri,omitempty"`
}

type MCPSessionInfo struct {
	ID string `json:"id,omitempty"`
}

type MCPInfo struct {
	Server   string           `json:"server,omitempty"`
	Tool     string           `json:"tool,omitempty"`
	Method   *MCPMethodInfo   `json:"method,omitempty"`
	Protocol *MCPProtocolInfo `json:"protocol,omitempty"`
	Resource *MCPResourceInfo `json:"resource,omitempty"`
	Session  *MCPSessionInfo  `json:"session,omitempty"`
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
	Text   string `json:"text,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Length int    `json:"length,omitempty"`
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

type GenAIAgentInfo struct {
	Description string `json:"description,omitempty"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
}

type GenAIConversationInfo struct {
	ID string `json:"id,omitempty"`
}

type GenAIDataSourceInfo struct {
	ID string `json:"id,omitempty"`
}

type GenAIEmbeddingsInfo struct {
	DimensionCount *int `json:"dimension_count,omitempty"`
}

type GenAIEvaluationScoreInfo struct {
	Label string   `json:"label,omitempty"`
	Value *float64 `json:"value,omitempty"`
}

type GenAIEvaluationInfo struct {
	Explanation string                    `json:"explanation,omitempty"`
	Name        string                    `json:"name,omitempty"`
	Score       *GenAIEvaluationScoreInfo `json:"score,omitempty"`
}

type GenAIInputInfo struct {
	Messages interface{} `json:"messages,omitempty"`
}

type GenAIOperationInfo struct {
	Name string `json:"name,omitempty"`
}

type GenAIOutputInfo struct {
	Messages interface{} `json:"messages,omitempty"`
	Type     string      `json:"type,omitempty"`
}

type GenAIPromptInfo struct {
	Name string `json:"name,omitempty"`
}

type GenAIProviderInfo struct {
	Name string `json:"name,omitempty"`
}

type GenAIRequestInfo struct {
	ChoiceCount      *int     `json:"choice_count,omitempty"`
	EncodingFormats  []string `json:"encoding_formats,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	Model            string   `json:"model,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	Seed             *int     `json:"seed,omitempty"`
	StopSequences    []string `json:"stop_sequences,omitempty"`
	Stream           *bool    `json:"stream,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopK             *float64 `json:"top_k,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
}

type GenAIResponseInfo struct {
	FinishReasons    []string `json:"finish_reasons,omitempty"`
	ID               string   `json:"id,omitempty"`
	Model            string   `json:"model,omitempty"`
	TimeToFirstChunk *float64 `json:"time_to_first_chunk,omitempty"`
}

type GenAIRetrievalInfo struct {
	Documents interface{} `json:"documents,omitempty"`
	QueryText string      `json:"query_text,omitempty"`
}

type GenAITokenInfo struct {
	Type string `json:"type,omitempty"`
}

type GenAIToolCallInfo struct {
	Arguments interface{} `json:"arguments,omitempty"`
	ID        string      `json:"id,omitempty"`
	Result    interface{} `json:"result,omitempty"`
}

type GenAIToolInfo struct {
	Call        *GenAIToolCallInfo `json:"call,omitempty"`
	Definitions interface{}        `json:"definitions,omitempty"`
	Description string             `json:"description,omitempty"`
	Name        string             `json:"name,omitempty"`
	Type        string             `json:"type,omitempty"`
}

type GenAIUsageCacheCreationInfo struct {
	InputTokens *int64 `json:"input_tokens,omitempty"`
}

type GenAIUsageCacheReadInfo struct {
	InputTokens *int64 `json:"input_tokens,omitempty"`
}

type GenAIUsageReasoningInfo struct {
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

// GenAIUsageInfo mirrors the OpenTelemetry GenAI semconv usage attribute
// names. Token counts are int64 to match OTLP integer values. CostUSD has no
// semconv equivalent; it carries runtime-reported USD cost only (for example
// Claude Code's cost metric) and is never derived from local pricing tables.
type GenAIUsageInfo struct {
	CacheCreation *GenAIUsageCacheCreationInfo `json:"cache_creation,omitempty"`
	CacheRead     *GenAIUsageCacheReadInfo     `json:"cache_read,omitempty"`
	CostUSD       *float64                     `json:"cost_usd,omitempty"`
	InputTokens   *int64                       `json:"input_tokens,omitempty"`
	OutputTokens  *int64                       `json:"output_tokens,omitempty"`
	Reasoning     *GenAIUsageReasoningInfo     `json:"reasoning,omitempty"`
}

type GenAIWorkflowInfo struct {
	Name string `json:"name,omitempty"`
}

type GenAIInfo struct {
	Agent              *GenAIAgentInfo        `json:"agent,omitempty"`
	Conversation       *GenAIConversationInfo `json:"conversation,omitempty"`
	DataSource         *GenAIDataSourceInfo   `json:"data_source,omitempty"`
	Embeddings         *GenAIEmbeddingsInfo   `json:"embeddings,omitempty"`
	Evaluation         *GenAIEvaluationInfo   `json:"evaluation,omitempty"`
	Input              *GenAIInputInfo        `json:"input,omitempty"`
	Operation          *GenAIOperationInfo    `json:"operation,omitempty"`
	Output             *GenAIOutputInfo       `json:"output,omitempty"`
	Prompt             *GenAIPromptInfo       `json:"prompt,omitempty"`
	Provider           *GenAIProviderInfo     `json:"provider,omitempty"`
	Request            *GenAIRequestInfo      `json:"request,omitempty"`
	Response           *GenAIResponseInfo     `json:"response,omitempty"`
	Retrieval          *GenAIRetrievalInfo    `json:"retrieval,omitempty"`
	SystemInstructions interface{}            `json:"system_instructions,omitempty"`
	Token              *GenAITokenInfo        `json:"token,omitempty"`
	Tool               *GenAIToolInfo         `json:"tool,omitempty"`
	Usage              *GenAIUsageInfo        `json:"usage,omitempty"`
	Workflow           *GenAIWorkflowInfo     `json:"workflow,omitempty"`
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
	Origin        Origin                 `json:"origin,omitempty"`
	Run           *RunInfo               `json:"run,omitempty"`
	Session       *SessionInfo           `json:"session,omitempty"`
	Trace         *TraceInfo             `json:"trace,omitempty"`
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
	GenAI         *GenAIInfo             `json:"gen_ai,omitempty"`
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
	if e.Content != nil && e.Content.Retention != "" {
		switch e.Content.Retention {
		case ContentRetentionMetadata, ContentRetentionRedacted, ContentRetentionFull:
		default:
			return errors.New("content.retention must be metadata, redacted, or full")
		}
	}
	return nil
}
