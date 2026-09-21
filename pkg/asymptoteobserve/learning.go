package asymptoteobserve

const LearningSchemaVersion = "beacon.learning.v1"

const (
	LearningEvaluationStatusDryRun    = "dry_run"
	LearningEvaluationStatusCompleted = "completed"
	LearningEvaluationStatusFailed    = "failed"
)

const (
	LearningCandidateStateCandidate  = "candidate"
	LearningCandidateStateApproved   = "approved"
	LearningCandidateStateRejected   = "rejected"
	LearningCandidateStateSuperseded = "superseded"
)

const (
	LearningMemoryKindWorkflow         = "workflow"
	LearningMemoryKindCorrection       = "correction"
	LearningMemoryKindDebuggingPattern = "debugging_pattern"
	LearningMemoryKindGotcha           = "gotcha"
	LearningMemoryKindConvention       = "convention"
)

type LearningProjectV1 struct {
	ID        string `json:"id"`
	Path      string `json:"path,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

type LearningTraceRefV1 struct {
	ID       string             `json:"id"`
	Title    string             `json:"title,omitempty"`
	Harness  TraceHarnessV1     `json:"harness"`
	Session  *TraceSessionV1    `json:"session,omitempty"`
	Repo     *TraceRepositoryV1 `json:"repository,omitempty"`
	EventIDs []string           `json:"event_ids,omitempty"`
}

type LearningEvaluationQuestionV1 struct {
	ID          string  `json:"id"`
	Prompt      string  `json:"prompt"`
	Probability float64 `json:"probability"`
	Confidence  float64 `json:"confidence,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

type LearningEvaluationV1 struct {
	SchemaVersion   string                         `json:"schema_version"`
	ID              string                         `json:"id"`
	Status          string                         `json:"status"`
	CreatedAt       string                         `json:"created_at"`
	UpdatedAt       string                         `json:"updated_at"`
	Project         LearningProjectV1              `json:"project"`
	RubricVersion   string                         `json:"rubric_version"`
	RubricHash      string                         `json:"rubric_hash"`
	Evaluator       string                         `json:"evaluator"`
	DryRun          bool                           `json:"dry_run,omitempty"`
	Trace           LearningTraceRefV1             `json:"trace"`
	Questions       []LearningEvaluationQuestionV1 `json:"questions,omitempty"`
	Score           float64                        `json:"score,omitempty"`
	CostEstimateUSD float64                        `json:"cost_estimate_usd,omitempty"`
	Error           string                         `json:"error,omitempty"`
}

type LearningEvidenceV1 struct {
	TraceID     string   `json:"trace_id"`
	EventIDs    []string `json:"event_ids,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Redacted    bool     `json:"redacted,omitempty"`
	Truncated   bool     `json:"truncated,omitempty"`
	ContentHash string   `json:"content_hash,omitempty"`
}

type LearningCandidateV1 struct {
	SchemaVersion      string               `json:"schema_version"`
	ID                 string               `json:"id"`
	MemoryID           string               `json:"memory_id,omitempty"`
	State              string               `json:"state"`
	Kind               string               `json:"kind"`
	Title              string               `json:"title"`
	Body               string               `json:"body"`
	Applicability      string               `json:"applicability,omitempty"`
	Tags               []string             `json:"tags,omitempty"`
	Project            LearningProjectV1    `json:"project"`
	SourceEvaluationID string               `json:"source_evaluation_id,omitempty"`
	Evidence           []LearningEvidenceV1 `json:"evidence"`
	CreatedAt          string               `json:"created_at"`
	UpdatedAt          string               `json:"updated_at"`
	ApprovedAt         string               `json:"approved_at,omitempty"`
	RejectedAt         string               `json:"rejected_at,omitempty"`
	SupersededAt       string               `json:"superseded_at,omitempty"`
	SupersededBy       string               `json:"superseded_by,omitempty"`
	ReviewReason       string               `json:"review_reason,omitempty"`
}

type LearningMemoryV1 struct {
	SchemaVersion string               `json:"schema_version"`
	ID            string               `json:"id"`
	CandidateID   string               `json:"candidate_id"`
	Kind          string               `json:"kind"`
	Title         string               `json:"title"`
	Body          string               `json:"body"`
	Applicability string               `json:"applicability,omitempty"`
	Tags          []string             `json:"tags,omitempty"`
	Project       LearningProjectV1    `json:"project"`
	Evidence      []LearningEvidenceV1 `json:"evidence"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
	SupersededBy  string               `json:"superseded_by,omitempty"`
}

type LearningStatusV1 struct {
	SchemaVersion    string `json:"schema_version"`
	Path             string `json:"path"`
	Evaluations      int    `json:"evaluations"`
	Candidates       int    `json:"candidates"`
	ApprovedMemories int    `json:"approved_memories"`
	SizeBytes        int64  `json:"size_bytes"`
}
