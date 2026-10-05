package pisession

import (
	"encoding/json"
	"testing"
)

// Pi's Usage type carries cacheWrite1h, the subset of cacheWrite written with one-hour retention,
// which its Anthropic provider fills from usage.cache_creation.ephemeral_1h_input_tokens. The
// session file stores the assistant message's usage as written.
func TestPiUsageCarriesTheOneHourCacheWriteSplit(t *testing.T) {
	cases := []struct {
		name    string
		usage   string
		oneHour *int64
	}{
		{
			name:    "anthropic with one-hour writes",
			usage:   `{"input":3,"output":120,"cacheRead":110000,"cacheWrite":20000,"cacheWrite1h":16000,"totalTokens":130123,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0.21}}`,
			oneHour: int64p(16000),
		},
		{
			// Providers other than Anthropic leave the field out.
			name:  "no split",
			usage: `{"input":3,"output":120,"cacheRead":0,"cacheWrite":500,"totalTokens":623,"cost":{"total":0.01}}`,
		},
		{
			name:    "split exceeds cacheWrite",
			usage:   `{"input":3,"output":1,"cacheRead":0,"cacheWrite":500,"cacheWrite1h":900,"totalTokens":504,"cost":{"total":0.01}}`,
			oneHour: int64p(500),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(tc.usage), &raw); err != nil {
				t.Fatal(err)
			}
			usage := piUsage(raw)
			if usage == nil || usage.CacheCreation == nil || usage.CacheCreation.InputTokens == nil {
				t.Fatalf("usage = %+v, want cache writes", usage)
			}
			got := usage.CacheCreation.Ephemeral1hInputTokens
			switch {
			case tc.oneHour == nil && got != nil:
				t.Fatalf("one-hour split = %d, want absent", *got)
			case tc.oneHour != nil && (got == nil || *got != *tc.oneHour):
				t.Fatalf("one-hour split = %v, want %d", got, *tc.oneHour)
			}
		})
	}
}

func int64p(v int64) *int64 { return &v }
