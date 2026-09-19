package opencodesession

type SourceKind string

const (
	SourceSQLite SourceKind = "sqlite"
	SourceLegacy SourceKind = "legacy"
)

type TraceRef struct {
	ID               string
	Kind             SourceKind
	SourcePath       string
	Title            string
	Preview          string
	Directory        string
	UpdatedAtUnixMS  int64
	CreatedAtUnixMS  int64
	SizeBytes        int64
	RelatedTo        string
	RelationshipType string
}

type Record struct {
	Order       int
	NativeID    string
	TimestampMS int64
	Type        string

	Role    string
	Content string
	ModelID string

	ToolName string
	CallID   string
	Args     map[string]interface{}
	Output   interface{}
	Status   string

	Tokens *TokenUsage
	Raw    map[string]interface{}
}

type TokenUsage struct {
	Input      int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
	CostUSD    float64
}
