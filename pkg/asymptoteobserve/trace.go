package asymptoteobserve

const TraceSchemaVersion = "beacon.trace.v1"

type TraceBundleV1 struct {
	SchemaVersion string            `json:"schema_version"`
	ID            string            `json:"id"`
	Summary       TraceSummaryV1    `json:"summary"`
	Events        []TraceEventV1    `json:"events"`
	Spans         []TraceSpanV1     `json:"spans,omitempty"`
	Range         *TraceRangeV1     `json:"range,omitempty"`
	Provenance    TraceProvenanceV1 `json:"provenance"`
}

type TraceProvenanceV1 struct {
	Source        string `json:"source"`
	GeneratedAt   string `json:"generated_at"`
	BeaconVersion string `json:"beacon_version,omitempty"`
}

type TraceSummaryV1 struct {
	ID                 string                `json:"id"`
	Title              string                `json:"title,omitempty"`
	Preview            string                `json:"preview,omitempty"`
	StartedAt          string                `json:"started_at,omitempty"`
	EndedAt            string                `json:"ended_at,omitempty"`
	UpdatedAt          string                `json:"updated_at,omitempty"`
	EventCount         int                   `json:"event_count"`
	LocalEventCount    int                   `json:"local_event_count,omitempty"`
	SharedEventCount   int                   `json:"shared_event_count,omitempty"`
	RemoteEventCount   int                   `json:"remote_event_count,omitempty"`
	LocalMessageCount  int                   `json:"local_message_count,omitempty"`
	RemoteMessageCount int                   `json:"remote_message_count,omitempty"`
	Harness            TraceHarnessV1        `json:"harness"`
	Session            *TraceSessionV1       `json:"session,omitempty"`
	Trace              *TraceIdentityV1      `json:"trace,omitempty"`
	Repository         *TraceRepositoryV1    `json:"repository,omitempty"`
	Namespace          *TraceNamespaceV1     `json:"namespace,omitempty"`
	Model              *TraceModelV1         `json:"model,omitempty"`
	TokenUsage         *TraceUsageV1         `json:"token_usage,omitempty"`
	Remote             *TraceRemoteV1        `json:"remote,omitempty"`
	Content            TraceContentSummaryV1 `json:"content"`
	Sharing            TraceSharingV1        `json:"sharing"`
}

type TraceHarnessV1 struct {
	Name              string   `json:"name"`
	Version           string   `json:"version,omitempty"`
	CollectionMethods []string `json:"collection_methods,omitempty"`
}

type TraceSessionV1 struct {
	ID               string `json:"id,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

type TraceIdentityV1 struct {
	ID         string `json:"id,omitempty"`
	RootSpanID string `json:"root_span_id,omitempty"`
}

type TraceRepositoryV1 struct {
	RemoteURL string `json:"remote_url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Path      string `json:"path,omitempty"`
}

type TraceNamespaceV1 struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name,omitempty"`
}

type TraceModelV1 struct {
	Names   []string `json:"names,omitempty"`
	Primary string   `json:"primary,omitempty"`
}

type TraceRemoteV1 struct {
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	SyncedAt  string `json:"synced_at,omitempty"`
}

type TraceSharingV1 struct {
	State           string        `json:"state"`
	Visibility      string        `json:"visibility"`
	URL             string        `json:"url,omitempty"`
	SharedAt        string        `json:"shared_at,omitempty"`
	LastRefreshedAt string        `json:"last_refreshed_at,omitempty"`
	CreatedBy       *TraceActorV1 `json:"created_by,omitempty"`
}

type TraceActorV1 struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
}

type TraceContentSummaryV1 struct {
	Retention      string `json:"retention"`
	HasRedactions  bool   `json:"has_redactions"`
	HasTruncations bool   `json:"has_truncations"`
}

type TraceEventV1 struct {
	ID            string             `json:"id"`
	Number        int                `json:"number"`
	Timestamp     string             `json:"timestamp"`
	Type          string             `json:"type"`
	Action        string             `json:"action"`
	Category      string             `json:"category,omitempty"`
	Fidelity      string             `json:"fidelity,omitempty"`
	Trace         *TraceEventTraceV1 `json:"trace,omitempty"`
	ToolCallID    string             `json:"tool_call_id,omitempty"`
	Actor         string             `json:"actor,omitempty"`
	Title         string             `json:"title,omitempty"`
	Summary       string             `json:"summary,omitempty"`
	Content       *TraceContentV1    `json:"content,omitempty"`
	Tool          *TraceToolV1       `json:"tool,omitempty"`
	Command       *TraceCommandV1    `json:"command,omitempty"`
	File          *TraceFileV1       `json:"file,omitempty"`
	MCP           *TraceMCPV1        `json:"mcp,omitempty"`
	Approval      *TraceApprovalV1   `json:"approval,omitempty"`
	Model         string             `json:"model,omitempty"`
	Usage         *TraceUsageV1      `json:"usage,omitempty"`
	SourceEventID string             `json:"source_event_id"`
}

type TraceEventTraceV1 struct {
	ID           string `json:"id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
}

type TraceContentV1 struct {
	Text      string      `json:"text,omitempty"`
	JSON      interface{} `json:"json,omitempty"`
	Retention string      `json:"retention"`
	Included  bool        `json:"included"`
	Redacted  bool        `json:"redacted,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Hash      string      `json:"hash,omitempty"`
	Bytes     int         `json:"bytes,omitempty"`
}

type TraceToolV1 struct {
	Name      string      `json:"name,omitempty"`
	Command   string      `json:"command,omitempty"`
	Path      string      `json:"path,omitempty"`
	Arguments interface{} `json:"arguments,omitempty"`
	Result    interface{} `json:"result,omitempty"`
}

type TraceCommandV1 struct {
	Command    string          `json:"command,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Output     *TraceContentV1 `json:"output,omitempty"`
}

type TraceFileV1 struct {
	Path      string          `json:"path,omitempty"`
	Operation string          `json:"operation,omitempty"`
	Language  string          `json:"language,omitempty"`
	Diff      *TraceContentV1 `json:"diff,omitempty"`
	DiffHash  string          `json:"diff_hash,omitempty"`
	DiffBytes int             `json:"diff_bytes,omitempty"`
}

type TraceMCPV1 struct {
	Server      string `json:"server,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Method      string `json:"method,omitempty"`
	ResourceURI string `json:"resource_uri,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

type TraceApprovalV1 struct {
	Required bool   `json:"required,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type TraceUsageV1 struct {
	InputTokens              int64   `json:"input_tokens,omitempty"`
	OutputTokens             int64   `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	ReasoningOutputTokens    int64   `json:"reasoning_output_tokens,omitempty"`
	CostUSD                  float64 `json:"cost_usd,omitempty"`
}

type TraceSpanV1 struct {
	ID           string   `json:"id"`
	ParentSpanID string   `json:"parent_span_id,omitempty"`
	TraceID      string   `json:"trace_id,omitempty"`
	Name         string   `json:"name,omitempty"`
	EventIDs     []string `json:"event_ids,omitempty"`
}

type TraceRangeV1 struct {
	TotalEvents    int `json:"total_events"`
	ReturnedEvents int `json:"returned_events"`
	Offset         int `json:"offset"`
	Limit          int `json:"limit,omitempty"`
	AroundEvent    int `json:"around_event,omitempty"`
}

type TraceListResultV1 struct {
	Traces       []TraceSummaryV1  `json:"traces"`
	TotalMatched int               `json:"total_matched"`
	Returned     int               `json:"returned"`
	Limit        int               `json:"limit"`
	Page         int               `json:"page,omitempty"`
	Truncated    bool              `json:"truncated"`
	Filters      map[string]string `json:"filters,omitempty"`
}

type TraceSearchResultV1 struct {
	Traces       []TraceSummaryV1    `json:"traces,omitempty"`
	Events       []TraceEventMatchV1 `json:"events,omitempty"`
	ResultLevel  string              `json:"result_level"`
	TotalMatched int                 `json:"total_matched"`
	Returned     int                 `json:"returned"`
	Limit        int                 `json:"limit"`
	Filters      map[string]string   `json:"filters,omitempty"`
}

type TraceEventMatchV1 struct {
	Trace   TraceSummaryV1 `json:"trace"`
	Event   TraceEventV1   `json:"event"`
	Snippet string         `json:"snippet,omitempty"`
	Score   int            `json:"score,omitempty"`
}

type TraceShowResultV1 struct {
	Trace  TraceSummaryV1 `json:"trace"`
	Events []TraceEventV1 `json:"events"`
	Spans  []TraceSpanV1  `json:"spans,omitempty"`
	Range  TraceRangeV1   `json:"range"`
}
