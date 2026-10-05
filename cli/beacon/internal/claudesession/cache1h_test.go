package claudesession

import (
	"encoding/json"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

// claudeUsageLine is an assistant record carrying usage exactly as Claude Code writes it to the
// session file: the API response's usage block, including the per-TTL cache_creation breakdown
// beside the flat cache_creation_input_tokens count.
func claudeUsageLine(uuid, usage string) string {
	return `{"parentUuid":"u1","isSidechain":false,"type":"assistant","message":{"model":"claude-opus-4-6","id":"msg_` + uuid + `","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":` + usage + `},"uuid":` + quote(uuid) + `,"timestamp":"2026-09-19T22:00:01.000Z","cwd":"/tmp/repo","sessionId":"sess-1","version":"2.1.154","gitBranch":"main"}`
}

func mappedCacheCreation(t *testing.T, usage string) map[string]interface{} {
	t.Helper()
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl", ProjectPath: "/tmp/repo"}
	mapped := MapSession(ref, decodeFixture(t, []string{claudeUsageLine("a1", usage)}), MapOptions{})
	ev := findAction(t, mapped, "token.usage")
	encoded, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		GenAI struct {
			Usage struct {
				CacheCreation map[string]interface{} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"gen_ai"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.GenAI.Usage.CacheCreation
}

func TestUsageCarriesTheOneHourCacheWriteSplit(t *testing.T) {
	cases := []struct {
		name    string
		usage   string
		total   interface{}
		oneHour interface{} // nil: absent
	}{
		{
			name:    "one-hour and five-minute writes",
			usage:   `{"input_tokens":3,"cache_creation_input_tokens":20000,"cache_read_input_tokens":110000,"cache_creation":{"ephemeral_5m_input_tokens":4000,"ephemeral_1h_input_tokens":16000},"output_tokens":120,"service_tier":"standard"}`,
			total:   float64(20000),
			oneHour: float64(16000),
		},
		{
			name:    "all five-minute writes",
			usage:   `{"input_tokens":3,"cache_creation_input_tokens":500,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":500,"ephemeral_1h_input_tokens":0},"output_tokens":5}`,
			total:   float64(500),
			oneHour: float64(0),
		},
		{
			// Older Claude Code builds wrote no breakdown; absent stays absent, not zero.
			name:  "no breakdown",
			usage: `{"input_tokens":3,"cache_creation_input_tokens":500,"output_tokens":5}`,
			total: float64(500),
		},
		{
			// A breakdown larger than the count it breaks down is clamped to the count.
			name:    "breakdown exceeds the write count",
			usage:   `{"input_tokens":3,"cache_creation_input_tokens":500,"cache_creation":{"ephemeral_1h_input_tokens":900},"output_tokens":5}`,
			total:   float64(500),
			oneHour: float64(500),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mappedCacheCreation(t, tc.usage)
			if got["input_tokens"] != tc.total {
				t.Fatalf("cache_creation = %v, want input_tokens %v", got, tc.total)
			}
			oneHour, present := got["ephemeral_1h_input_tokens"]
			if tc.oneHour == nil {
				if present {
					t.Fatalf("cache_creation = %v, want no one-hour split", got)
				}
				return
			}
			if oneHour != tc.oneHour {
				t.Fatalf("cache_creation = %v, want ephemeral_1h_input_tokens %v", got, tc.oneHour)
			}
		})
	}
}

// TestUsageDeltaCarriesTheOneHourSubset covers the correction a later sweep emits when it finds a
// larger snapshot of a response an earlier sweep already counted: the one-hour subset of the cache
// writes is differenced with the writes, so a split response prices its 1h writes exactly once.
func TestUsageDeltaCarriesTheOneHourSubset(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	prior := &schema.GenAIUsageInfo{
		InputTokens:   i64(4),
		OutputTokens:  i64(8),
		CacheCreation: &schema.GenAIUsageCacheCreationInfo{InputTokens: i64(1200), Ephemeral1hInputTokens: i64(1000)},
	}
	best := &schema.GenAIUsageInfo{
		InputTokens:   i64(4),
		OutputTokens:  i64(300),
		CacheCreation: &schema.GenAIUsageCacheCreationInfo{InputTokens: i64(2000), Ephemeral1hInputTokens: i64(1600)},
	}
	delta := usageDelta(best, prior)
	if got := delta.CacheCreation; got == nil || got.InputTokens == nil || *got.InputTokens != 800 ||
		got.Ephemeral1hInputTokens == nil || *got.Ephemeral1hInputTokens != 600 {
		t.Fatalf("delta cache_creation = %+v, want 800 writes of which 600 one-hour", got)
	}

	// End to end over every split point of a streamed response: the one-hour writes are counted
	// once in total, whichever sweep emits them.
	ref := SessionRef{ID: "sess-1", Path: "/tmp/sess-1.jsonl"}
	lines := []string{userLine("sess-1", "p", "go")}
	lines = append(lines, splitResponse("sess-1", "r1", "msg_01H", "req_01H", 300)...)
	records := decodeFixture(t, lines)
	for cut := 1; cut <= len(records); cut++ {
		first := MapSession(ref, records[:cut], MapOptions{})
		second := MapSession(ref, records, MapOptions{MinLine: records[cut-1].Line, SkipSessionStarted: true})
		var oneHour int64
		for _, item := range append(usageEvents(first), usageEvents(second)...) {
			oneHour += item.Event.GenAI.Usage.CacheCreation.OneHourInputTokens()
		}
		if oneHour != 1200 {
			t.Errorf("cut after line %d: one-hour writes counted %d, want 1200", records[cut-1].Line, oneHour)
		}
	}
}
