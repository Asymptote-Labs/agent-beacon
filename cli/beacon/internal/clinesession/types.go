// Package clinesession reads Cline's local session store and turns committed history into Beacon
// endpoint telemetry.
//
// This is intentionally separate from the live Cline plugin integration. The plugin observes Cline
// while a task is running; this package polls files Cline already wrote under ~/.cline and the VS
// Code extension's global storage. Every event emitted here is marked harness.collection_method=poll.
package clinesession

import "encoding/json"

const Harness = "cline"

const (
	SourceHistory  = "history"
	SourceMessages = "messages"
	SourceKanban   = "kanban"
)

type TraceRef struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	SourcePath       string `json:"source_path"`
	Title            string `json:"title,omitempty"`
	Preview          string `json:"preview,omitempty"`
	Directory        string `json:"directory,omitempty"`
	UpdatedAtUnixMS  int64  `json:"updated_at_ms,omitempty"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	RelatedTo        string `json:"related_to,omitempty"`
	RelationshipType string `json:"relationship_type,omitempty"`
}

type Entry struct {
	Order     int
	Role      string
	Content   interface{}
	Text      string
	Message   string
	ID        string
	Model     string
	ModelID   string
	ModelInfo map[string]interface{}
	Metrics   map[string]interface{}
	Usage     map[string]interface{}
	Timestamp interface{}
	Raw       map[string]interface{}
}

type Record struct {
	Order       int
	Type        string
	TimestampMS int64
	Content     string
	ModelID     string
	ToolName    string
	CallID      string
	Args        map[string]interface{}
	Output      interface{}
	OutputText  string
	Status      string
	Tokens      *TokenUsage
	Raw         map[string]interface{}
}

type TokenUsage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	CostUSD    float64
}

func parseEntry(data map[string]interface{}, order int) Entry {
	entry := Entry{Order: order, Raw: data}
	entry.Role = stringValue(data["role"])
	entry.Content = data["content"]
	entry.Text = stringValue(data["text"])
	entry.Message = stringValue(data["message"])
	entry.ID = firstNonEmpty(stringValue(data["id"]), stringValue(data["uuid"]), stringValue(data["messageId"]))
	entry.Model = stringValue(data["model"])
	entry.ModelID = firstNonEmpty(stringValue(data["modelId"]), stringValue(data["model_id"]))
	entry.ModelInfo, _ = data["modelInfo"].(map[string]interface{})
	if entry.ModelInfo == nil {
		entry.ModelInfo, _ = data["model_info"].(map[string]interface{})
	}
	entry.Metrics, _ = data["metrics"].(map[string]interface{})
	entry.Usage, _ = data["usage"].(map[string]interface{})
	entry.Timestamp = firstNonNil(data["ts"], data["timestamp"], data["time"])
	return entry
}

func recordFromJSON(data json.RawMessage, order int) (Entry, bool) {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return Entry{}, false
	}
	return parseEntry(obj, order), true
}
