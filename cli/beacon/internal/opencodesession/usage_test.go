package opencodesession

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/tokens"
)

// The assistant messages below are shaped like OpenCode's stored
// message.data / storage/message JSON. Each pair of numbers is what the
// provider reported (outputTokens including reasoning, and reasoningTokens);
// the comments give what OpenCode wrote for it.
var opencodeUsageMessages = []struct {
	id            string
	tokens        map[string]interface{}
	cost          float64
	wantOutput    int64
	wantReasoning int64
}{
	{
		// v1.3.16 and later: output = outputTokens (1262) - reasoningTokens (850),
		// and total = inputTokens (1500, cache included) + outputTokens.
		id:            "msg_a_current",
		tokens:        map[string]interface{}{"total": 2762, "input": 1100, "output": 412, "reasoning": 850, "cache": map[string]interface{}{"read": 400, "write": 0}},
		cost:          0.031,
		wantOutput:    1262,
		wantReasoning: 850,
	},
	{
		// Before v1.3.16: output = outputTokens whole, reasoning already in it.
		id:            "msg_b_pre_1_3_16",
		tokens:        map[string]interface{}{"total": 2762, "input": 1100, "output": 1262, "reasoning": 850, "cache": map[string]interface{}{"read": 400, "write": 0}},
		cost:          0.052,
		wantOutput:    1262,
		wantReasoning: 850,
	},
	{
		// A model without reasoning is the same in every release.
		id:         "msg_c_no_reasoning",
		tokens:     map[string]interface{}{"total": 900, "input": 700, "output": 200, "reasoning": 0, "cache": map[string]interface{}{"read": 0, "write": 0}},
		cost:       0.004,
		wantOutput: 200,
	},
	{
		// Before v1.1.57 there was no total, and output was outputTokens whole.
		id:            "msg_d_no_total",
		tokens:        map[string]interface{}{"input": 300, "output": 90, "reasoning": 40, "cache": map[string]interface{}{"read": 0, "write": 10}},
		wantOutput:    90,
		wantReasoning: 40,
	},
}

// The sum of every message above, counting each reasoning token once.
const (
	wantInputTotal      = 1100 + 1100 + 700 + 300
	wantOutputTotal     = 1262 + 1262 + 200 + 90
	wantCacheTotal      = 400 + 400 + 0 + 10
	wantReasoningTotal  = 850 + 850 + 40
	wantTotalTokenCount = wantInputTotal + wantOutputTotal + wantCacheTotal
)

func TestSQLiteUsageCountsReasoningInsideOutputOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, directory TEXT, time_created INTEGER, time_updated INTEGER)`)
	mustExec(t, db, `CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`)
	mustExec(t, db, `CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, data TEXT)`)
	mustExec(t, db, `INSERT INTO session (id, title, directory, time_created, time_updated) VALUES ('ses_1', 'Reasoning', '/repo', 1770000000000, 1770000009000)`)
	for i, msg := range opencodeUsageMessages {
		created := int64(1770000000100 + i*1000)
		insertJSON(t, db, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`, msg.id, "ses_1", created, assistantMessage(msg.id, "ses_1", created, msg.tokens, msg.cost))
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertUsageCountsReasoningOnce(t, store)
}

func TestLegacyStoreUsageCountsReasoningInsideOutputOnce(t *testing.T) {
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage")
	writeJSONFile(t, filepath.Join(storage, "session", "proj_1", "ses_1.json"), map[string]interface{}{
		"id": "ses_1", "title": "Reasoning", "directory": "/repo", "version": "0.15.0",
		"time": map[string]interface{}{"created": 1770000000000, "updated": 1770000009000},
	})
	for i, msg := range opencodeUsageMessages {
		created := int64(1770000000100 + i*1000)
		writeJSONFile(t, filepath.Join(storage, "message", "ses_1", msg.id+".json"), assistantMessage(msg.id, "ses_1", created, msg.tokens, msg.cost))
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertUsageCountsReasoningOnce(t, store)
}

func assistantMessage(id, sessionID string, created int64, tokens map[string]interface{}, cost float64) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "sessionID": sessionID, "role": "assistant",
		"providerID": "openai", "modelID": "gpt-5", "finish": "stop",
		"time":   map[string]interface{}{"created": created, "completed": created + 500},
		"tokens": tokens,
		"cost":   cost,
	}
}

func writeJSONFile(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertUsageCountsReasoningOnce(t *testing.T, store *Store) {
	t.Helper()
	refs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	records, err := store.Read(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	mapped := MapTrace(refs[0], records, MapOptions{})
	var events []schema.Event
	for _, item := range mapped {
		if item.Event.Event.Action == "token.usage" {
			events = append(events, item.Event)
		}
	}
	if len(events) != len(opencodeUsageMessages) {
		t.Fatalf("usage events = %d, want %d", len(events), len(opencodeUsageMessages))
	}
	for i, want := range opencodeUsageMessages {
		usage := events[i].GenAI.Usage
		if usage.OutputTokens == nil || *usage.OutputTokens != want.wantOutput {
			t.Fatalf("%s output_tokens = %v, want %d", want.id, deref(usage.OutputTokens), want.wantOutput)
		}
		var reasoning int64
		if usage.Reasoning != nil && usage.Reasoning.OutputTokens != nil {
			reasoning = *usage.Reasoning.OutputTokens
		}
		if reasoning != want.wantReasoning {
			t.Fatalf("%s reasoning.output_tokens = %d, want %d", want.id, reasoning, want.wantReasoning)
		}
	}
	report := tokens.Aggregate(events, tokens.Options{})
	totals := report.Totals
	if totals.InputTokens != wantInputTotal || totals.OutputTokens != wantOutputTotal ||
		totals.CacheReadInputTokens+totals.CacheCreationInputTokens != wantCacheTotal ||
		totals.ReasoningOutputTokens != wantReasoningTotal {
		t.Fatalf("totals = %+v", totals)
	}
	if got := totals.TotalTokens(); got != wantTotalTokenCount {
		t.Fatalf("TotalTokens = %d, want %d (reasoning counted once, inside output)", got, wantTotalTokenCount)
	}
}

func deref(value *int64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}
