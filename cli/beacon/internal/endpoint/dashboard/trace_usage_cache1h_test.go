package dashboard

import (
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Trace usage carries the one-hour cache-write subset beside the write count, clamped to it, and
// sums it with the writes it belongs to.
func TestTraceUsageCarriesTheOneHourCacheWriteSubset(t *testing.T) {
	event := func(total, oneHour int64) schema.Event {
		claimed := oneHour
		return schema.Event{GenAI: &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{
			CacheCreation: &schema.GenAIUsageCacheCreationInfo{InputTokens: &total, Ephemeral1hInputTokens: &claimed},
		}}}
	}
	first := traceUsage(event(20000, 16000))
	if first == nil || first.CacheCreationInputTokens != 20000 || first.CacheCreation1hInputTokens != 16000 {
		t.Fatalf("usage = %+v, want 20000 writes of which 16000 one-hour", first)
	}
	// A log line claiming more one-hour writes than writes is clamped.
	second := traceUsage(event(500, 900))
	if second == nil || second.CacheCreation1hInputTokens != 500 {
		t.Fatalf("usage = %+v, want the subset clamped to 500", second)
	}
	sum := TraceUsageV1{}
	addTraceUsage(&sum, *first)
	addTraceUsage(&sum, *second)
	if sum.CacheCreationInputTokens != 20500 || sum.CacheCreation1hInputTokens != 16500 {
		t.Fatalf("sum = %+v", sum)
	}
	unsplit := traceUsage(schema.Event{GenAI: &schema.GenAIInfo{Usage: &schema.GenAIUsageInfo{
		CacheCreation: asymptoteobserve.NewCacheCreationUsage(300, nil),
	}}})
	if unsplit == nil || unsplit.CacheCreation1hInputTokens != 0 {
		t.Fatalf("usage = %+v, want no subset when unreported", unsplit)
	}
}
