package asymptoteobserve

import (
	"encoding/json"
	"testing"
)

func i64(v int64) *int64 { return &v }

func TestCacheCreationOneHourSubsetRoundTrips(t *testing.T) {
	line := `{"gen_ai":{"usage":{"input_tokens":3,"cache_creation":{"input_tokens":20000,"ephemeral_1h_input_tokens":16000}}}}`
	var event Event
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatal(err)
	}
	c := event.GenAI.Usage.CacheCreation
	if c == nil || c.InputTokens == nil || *c.InputTokens != 20000 || c.Ephemeral1hInputTokens == nil || *c.Ephemeral1hInputTokens != 16000 {
		t.Fatalf("cache_creation = %+v", c)
	}
	encoded, err := json.Marshal(event.GenAI.Usage)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"cache_creation":{"input_tokens":20000,"ephemeral_1h_input_tokens":16000},"input_tokens":3}`; string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}

	// Additive: a block without the split encodes exactly as before.
	encoded, _ = json.Marshal(&GenAIUsageCacheCreationInfo{InputTokens: i64(5)})
	if string(encoded) != `{"input_tokens":5}` {
		t.Fatalf("encoded = %s, want the field omitted", encoded)
	}
}

func TestNewCacheCreationUsageClampsTheSubset(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		oneHour *int64
		want    *int64
	}{
		{"unreported", 100, nil, nil},
		{"within", 100, i64(40), i64(40)},
		{"equal", 100, i64(100), i64(100)},
		{"above", 100, i64(140), i64(100)},
		{"negative", 100, i64(-1), i64(0)},
		{"zero total", 0, i64(10), i64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewCacheCreationUsage(tc.total, tc.oneHour)
			if got.InputTokens == nil || *got.InputTokens != tc.total {
				t.Fatalf("input_tokens = %v, want %d", got.InputTokens, tc.total)
			}
			switch {
			case tc.want == nil && got.Ephemeral1hInputTokens != nil:
				t.Fatalf("subset = %d, want absent", *got.Ephemeral1hInputTokens)
			case tc.want != nil && (got.Ephemeral1hInputTokens == nil || *got.Ephemeral1hInputTokens != *tc.want):
				t.Fatalf("subset = %v, want %d", got.Ephemeral1hInputTokens, *tc.want)
			}
		})
	}
}

func TestOneHourInputTokensClampsWhatALogLineClaims(t *testing.T) {
	var nilBlock *GenAIUsageCacheCreationInfo
	cases := []struct {
		name  string
		block *GenAIUsageCacheCreationInfo
		want  int64
	}{
		{"nil block", nilBlock, 0},
		{"unreported", &GenAIUsageCacheCreationInfo{InputTokens: i64(10)}, 0},
		{"reported", &GenAIUsageCacheCreationInfo{InputTokens: i64(10), Ephemeral1hInputTokens: i64(4)}, 4},
		{"above the total", &GenAIUsageCacheCreationInfo{InputTokens: i64(10), Ephemeral1hInputTokens: i64(40)}, 10},
		{"no total", &GenAIUsageCacheCreationInfo{Ephemeral1hInputTokens: i64(40)}, 0},
	}
	for _, tc := range cases {
		if got := tc.block.OneHourInputTokens(); got != tc.want {
			t.Fatalf("%s: OneHourInputTokens = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCacheCreationMinusDifferencesBothCounts(t *testing.T) {
	val := func(p *int64) interface{} {
		if p == nil {
			return nil
		}
		return *p
	}
	cases := []struct {
		name          string
		next, prior   *GenAIUsageCacheCreationInfo
		total, oneHrs interface{}
	}{
		{"nothing counted", &GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)}, nil, int64(100), int64(60)},
		{"grown", &GenAIUsageCacheCreationInfo{InputTokens: i64(300), Ephemeral1hInputTokens: i64(200)},
			&GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)}, int64(200), int64(140)},
		{"repeated snapshot", &GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)},
			&GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)}, int64(0), int64(0)},
		{"prior without split", &GenAIUsageCacheCreationInfo{InputTokens: i64(300), Ephemeral1hInputTokens: i64(250)},
			&GenAIUsageCacheCreationInfo{InputTokens: i64(100)}, int64(200), int64(200)},
		{"next without split", &GenAIUsageCacheCreationInfo{InputTokens: i64(300)},
			&GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)}, int64(200), nil},
		{"shrunk", &GenAIUsageCacheCreationInfo{InputTokens: i64(50), Ephemeral1hInputTokens: i64(10)},
			&GenAIUsageCacheCreationInfo{InputTokens: i64(100), Ephemeral1hInputTokens: i64(60)}, int64(0), int64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.next.Minus(tc.prior)
			if val(got.InputTokens) != tc.total || val(got.Ephemeral1hInputTokens) != tc.oneHrs {
				t.Fatalf("Minus = {%v %v}, want {%v %v}", val(got.InputTokens), val(got.Ephemeral1hInputTokens), tc.total, tc.oneHrs)
			}
		})
	}
	var nilBlock *GenAIUsageCacheCreationInfo
	if nilBlock.Minus(&GenAIUsageCacheCreationInfo{InputTokens: i64(1)}) != nil {
		t.Fatal("nil minus anything should be nil")
	}
}
