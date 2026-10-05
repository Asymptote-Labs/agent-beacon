package claudesession

import (
	"encoding/json"
	"testing"
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
