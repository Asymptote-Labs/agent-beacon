package pricing

import (
	"math"
	"testing"
)

// The rates below are written out rather than read from the embedded catalog, so the golden
// figures are hand-checkable and do not move when the catalog is regenerated. They are the
// published list prices of the named models at the time of writing.
var (
	// Anthropic Claude Opus 4.6: $5 in, $25 out, $0.50 cache read, $6.25 5-minute write,
	// $10 one-hour write per MTok.
	opusRates = Rates{Provider: "anthropic", RateSet: RateSet{
		Input: 5_000_000, Output: 25_000_000, CacheRead: 500_000, CacheWrite: 6_250_000, CacheWrite1h: 10_000_000,
	}}
	// OpenAI GPT-5.4: $2.50 in, $15 out, $0.25 cached; above 272k prompt tokens $5, $22.50,
	// $0.50. OpenAI publishes no cache-write price.
	gpt54Rates = Rates{Provider: "openai",
		RateSet: RateSet{Input: 2_500_000, Output: 15_000_000, CacheRead: 250_000},
		Bands:   []Band{{AboveTokens: 272_000, RateSet: RateSet{Input: 5_000_000, Output: 22_500_000, CacheRead: 500_000}}},
	}
	// Gemini 2.5 Pro: $1.25 in, $10 out, $0.125 cached; above 200k $2.50, $15, $0.25.
	geminiRates = Rates{Provider: "gemini",
		RateSet: RateSet{Input: 1_250_000, Output: 10_000_000, CacheRead: 125_000},
		Bands:   []Band{{AboveTokens: 200_000, RateSet: RateSet{Input: 2_500_000, Output: 15_000_000, CacheRead: 250_000}}},
	}
)

func TestCostAnthropicWithOneHourWrites(t *testing.T) {
	tokens := Tokens{Input: 1_200, Output: 3_400, CacheRead: 150_000, CacheCreation: 20_000, CacheCreation1h: 8_000}
	// input      1,200 x $5/M     = $0.006000
	// 5m write  12,000 x $6.25/M  = $0.075000
	// 1h write   8,000 x $10/M    = $0.080000
	// read     150,000 x $0.50/M  = $0.075000
	// output     3,400 x $25/M    = $0.085000
	//                               $0.321000
	got := Cost(opusRates, tokens, true)
	if got.Cost != 321_000 {
		t.Fatalf("cost = %d, want 321000", got.Cost)
	}
	if got.CacheReadAtInputRate || got.CacheWriteAtInputRate || got.CacheWrite1hAtWriteRate || got.BandAboveTokens != 0 {
		t.Fatalf("unexpected fallback or band: %+v", got)
	}
	// The same tokens as five-minute writes only cost less: 20,000 x $6.25/M = $0.125.
	five := Cost(opusRates, Tokens{Input: 1_200, Output: 3_400, CacheRead: 150_000, CacheCreation: 20_000}, true)
	if five.Cost != 6_000+125_000+75_000+85_000 {
		t.Fatalf("five-minute cost = %d", five.Cost)
	}
}

func TestCostOpenAILongContextBandIsRequestScoped(t *testing.T) {
	// Prompt = 100,000 + 180,000 = 280,000 > 272,000.
	tokens := Tokens{Input: 100_000, CacheRead: 180_000, Output: 2_000}

	// One request: every token at the band rate.
	//   100,000 x $5/M = $0.50; 180,000 x $0.50/M = $0.09; 2,000 x $22.50/M = $0.045
	req := Cost(gpt54Rates, tokens, true)
	if req.Cost != 635_000 || req.BandAboveTokens != 272_000 {
		t.Fatalf("request-scoped = %+v, want 635000 in the 272k band", req)
	}

	// An aggregate of the same size is many smaller requests: base rates.
	//   100,000 x $2.50/M = $0.25; 180,000 x $0.25/M = $0.045; 2,000 x $15/M = $0.03
	agg := Cost(gpt54Rates, tokens, false)
	if agg.Cost != 325_000 || agg.BandAboveTokens != 0 {
		t.Fatalf("aggregate = %+v, want 325000 at base rates", agg)
	}

	// "Above 272k" is strict: a prompt of exactly 272,000 is base-priced.
	edge := Cost(gpt54Rates, Tokens{Input: 72_000, CacheRead: 200_000}, true)
	if edge.BandAboveTokens != 0 || edge.Cost != 180_000+50_000 {
		t.Fatalf("272,000-token prompt = %+v, want base 230000", edge)
	}
	over := Cost(gpt54Rates, Tokens{Input: 72_001, CacheRead: 200_000}, true)
	if over.BandAboveTokens != 272_000 {
		t.Fatalf("272,001-token prompt = %+v, want the band", over)
	}
}

func TestCostOpenAICacheWriteFallsBackToInput(t *testing.T) {
	// OpenAI bills a cache write as ordinary input; Cost does the same and says so.
	got := Cost(gpt54Rates, Tokens{CacheCreation: 10_000}, false)
	if got.Cost != 25_000 || !got.CacheWriteAtInputRate || got.CacheWrite1hAtWriteRate {
		t.Fatalf("got %+v, want 25000 at the input rate", got)
	}
	// One-hour writes with no published one-hour or write rate fall back twice, to input.
	got = Cost(gpt54Rates, Tokens{CacheCreation: 10_000, CacheCreation1h: 10_000}, false)
	if got.Cost != 25_000 || !got.CacheWriteAtInputRate || !got.CacheWrite1hAtWriteRate {
		t.Fatalf("got %+v", got)
	}
}

func TestCostGeminiBandCountsCacheWritesInThePrompt(t *testing.T) {
	// 210,000 uncached input tokens in one request: band. 210,000 x $2.50/M = $0.525,
	// 1,000 x $15/M = $0.015.
	got := Cost(geminiRates, Tokens{Input: 210_000, Output: 1_000}, true)
	if got.Cost != 540_000 || got.BandAboveTokens != 200_000 {
		t.Fatalf("got %+v, want 540000 in the 200k band", got)
	}
	// Aggregate: 210,000 x $1.25/M = $0.2625, 1,000 x $10/M = $0.01.
	if agg := Cost(geminiRates, Tokens{Input: 210_000, Output: 1_000}, false); agg.Cost != 272_500 {
		t.Fatalf("aggregate = %d, want 272500", agg.Cost)
	}
	// Cache writes are part of the prompt the threshold is measured on.
	mixed := Cost(geminiRates, Tokens{Input: 150_000, CacheCreation: 50_001}, true)
	if mixed.BandAboveTokens != 200_000 {
		t.Fatalf("150,000 input + 50,001 cache write = %+v, want the band", mixed)
	}
}

func TestCostPicksTheLargestBandBelowThePrompt(t *testing.T) {
	rates := Rates{
		RateSet: RateSet{Input: 1_000_000, Output: 1_000_000},
		Bands: []Band{
			{AboveTokens: 128_000, RateSet: RateSet{Input: 2_000_000, Output: 2_000_000}},
			{AboveTokens: 256_000, RateSet: RateSet{Input: 3_000_000, Output: 3_000_000}},
		},
	}
	for _, c := range []struct {
		prompt, band int64
	}{
		{128_000, 0},
		{128_001, 128_000},
		{256_000, 128_000},
		{256_001, 256_000},
		{1_000_000, 256_000},
	} {
		if got := Cost(rates, Tokens{Input: c.prompt}, true); got.BandAboveTokens != c.band {
			t.Errorf("prompt %d: band %d, want %d", c.prompt, got.BandAboveTokens, c.band)
		}
	}
}

func TestCostZeroTokens(t *testing.T) {
	got := Cost(opusRates, Tokens{}, true)
	if got != (Estimate{}) {
		t.Fatalf("zero tokens = %+v, want the zero Estimate", got)
	}
	// Fallback flags are about counts that were priced, so none are set for zero counts even
	// on a model that publishes no cache rates.
	if got := Cost(Rates{RateSet: RateSet{Input: 1, Output: 1}}, Tokens{}, false); got != (Estimate{}) {
		t.Fatalf("zero tokens without cache rates = %+v", got)
	}
}

func TestCostReasoningIsInsideOutput(t *testing.T) {
	// Reasoning alongside output is a breakdown of it: only the 500 output tokens are billed.
	both := Cost(opusRates, Tokens{Output: 500, Reasoning: 1_000}, false)
	if both.Cost != 12_500 || both.ReasoningAtOutputRate {
		t.Fatalf("output+reasoning = %+v, want 12500 (500 x $25/M)", both)
	}
	// A record carrying reasoning and no output is billed as that output.
	only := Cost(opusRates, Tokens{Reasoning: 1_000}, false)
	if only.Cost != 25_000 || !only.ReasoningAtOutputRate {
		t.Fatalf("reasoning only = %+v, want 25000 (1,000 x $25/M)", only)
	}
}

func TestCostRoundsHalfUpOnceAtTheEnd(t *testing.T) {
	// A rate of 1 microdollar per million tokens makes every token half a millionth of the
	// rounding unit visible.
	tiny := Rates{RateSet: RateSet{Input: 1, Output: 1}}
	if got := Cost(tiny, Tokens{Input: 500_000}, false).Cost; got != 1 {
		t.Fatalf("0.5 microdollar = %d, want 1 (half up)", got)
	}
	if got := Cost(tiny, Tokens{Input: 499_999}, false).Cost; got != 0 {
		t.Fatalf("0.499999 microdollar = %d, want 0", got)
	}
	// Two half-microdollar terms are one microdollar. Rounding each term would make it two.
	if got := Cost(tiny, Tokens{Input: 500_000, Output: 500_000}, false).Cost; got != 1 {
		t.Fatalf("0.5 + 0.5 microdollar = %d, want 1 (one rounding, at the end)", got)
	}
	if got := Cost(tiny, Tokens{Input: 1_500_000}, false).Cost; got != 2 {
		t.Fatalf("1.5 microdollar = %d, want 2 (half up, not half to even)", got)
	}
}

func TestCostHugeCountsDoNotOverflow(t *testing.T) {
	// 9e12 tokens x $5/M: the tokens x rate product (4.5e19) overflows int64 though the cost
	// ($45,000,000) does not. The arithmetic must still be exact.
	got := Cost(opusRates, Tokens{Input: 9_000_000_000_000}, false)
	if got.Cost != 45_000_000_000_000 || got.Saturated {
		t.Fatalf("9e12 input tokens = %+v, want exactly 45000000000000", got)
	}
	// A cost past the int64 range saturates and says so, rather than wrapping negative.
	all := Tokens{Input: math.MaxInt64, Output: math.MaxInt64, CacheRead: math.MaxInt64, CacheCreation: math.MaxInt64, CacheCreation1h: math.MaxInt64}
	big := Cost(opusRates, all, true)
	if !big.Saturated || big.Cost != math.MaxInt64 {
		t.Fatalf("max counts = %+v, want saturated", big)
	}
}

func TestCostClampsInconsistentCounts(t *testing.T) {
	// More one-hour writes than writes cannot happen; the excess is not billed.
	got := Cost(opusRates, Tokens{CacheCreation: 1_000, CacheCreation1h: 5_000}, false)
	if got.Cost != 10_000 { // 1,000 x $10/M
		t.Fatalf("1h > total = %d, want 10000", got.Cost)
	}
	// Negative counts (a counter reset differenced the wrong way) are zero, not a credit.
	neg := Cost(opusRates, Tokens{Input: -1_000_000, Output: 1_000}, false)
	if neg.Cost != 25_000 {
		t.Fatalf("negative input = %d, want 25000", neg.Cost)
	}
}

func TestCostCacheReadWithoutPublishedRate(t *testing.T) {
	// A model with no published cache-read price bills reads at its input price, and says so.
	rates := Rates{RateSet: RateSet{Input: 570_000, Output: 2_300_000}}
	got := Cost(rates, Tokens{CacheRead: 1_000_000}, false)
	if got.Cost != 570_000 || !got.CacheReadAtInputRate {
		t.Fatalf("got %+v, want 570000 at the input rate", got)
	}
}
