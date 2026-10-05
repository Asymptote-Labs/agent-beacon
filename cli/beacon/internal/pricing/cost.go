package pricing

// RateSet is one set of per-token prices, in microdollars per million tokens. Zero means the
// provider publishes no such rate (see "Missing rates" in the package docs), never "free".
type RateSet struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	// CacheRead is the price of a prompt-cache hit.
	CacheRead int64 `json:"cache_read,omitempty"`
	// CacheWrite is the price of writing the prompt cache with the default (five-minute) TTL.
	CacheWrite int64 `json:"cache_write,omitempty"`
	// CacheWrite1h is the price of a cache write with a one-hour TTL.
	CacheWrite1h int64 `json:"cache_write_1h,omitempty"`
}

// Band is a long-context price tier: a request whose prompt is larger than AboveTokens is
// billed at these rates for every token, not only for the tokens past the threshold. The
// generator writes complete tuples, so a field the provider did not restate for the band
// already carries the base rate.
type Band struct {
	AboveTokens int64 `json:"above_tokens"`
	RateSet
}

// Rates is everything the catalog knows about one model's price.
type Rates struct {
	// Provider is the upstream list's provider for the entry (anthropic, openai, gemini,
	// vertex_ai-anthropic_models, bedrock_converse, ...), kept so a report can say whose list
	// price it used.
	Provider string `json:"provider"`
	RateSet
	// Bands are sorted by strictly increasing AboveTokens.
	Bands []Band `json:"bands,omitempty"`
}

// Tokens is one usage record in gen_ai.usage terms. See "Token semantics" in the package docs:
// the counts are disjoint, CacheCreation1h is a subset of CacheCreation, and Reasoning is a
// breakdown of Output.
type Tokens struct {
	Input           int64
	Output          int64
	CacheRead       int64
	CacheCreation   int64
	CacheCreation1h int64
	Reasoning       int64
}

// Estimate is a list-price cost and how it was reached.
type Estimate struct {
	Cost Microdollars
	// BandAboveTokens is the threshold of the long-context band that priced the request, or
	// zero when base rates applied.
	BandAboveTokens int64
	// The fallbacks below are set only when they priced a non-zero count.
	//
	// CacheReadAtInputRate: the model publishes no cache-read rate, so reads were priced as
	// input.
	CacheReadAtInputRate bool
	// CacheWriteAtInputRate: the model publishes no cache-write rate, so writes were priced
	// as input.
	CacheWriteAtInputRate bool
	// CacheWrite1hAtWriteRate: the model publishes no one-hour write rate, so one-hour writes
	// were priced at the five-minute write rate.
	CacheWrite1hAtWriteRate bool
	// ReasoningAtOutputRate: Output was zero and Reasoning was not, so reasoning tokens were
	// priced as the output they are.
	ReasoningAtOutputRate bool
	// Saturated: the true cost does not fit in an int64 of microdollars (about 9.2 trillion
	// dollars), so Cost holds the maximum. It means the input is corrupt; do not print Cost.
	Saturated bool
}

// Cost prices t at r.
//
// requestScoped says that t is exactly one API request. Only then can a long-context band
// apply: the band is chosen by the request's prompt size, Input + CacheRead + CacheCreation,
// and is the one with the largest AboveTokens strictly below it. Any sum of requests --
// a turn, a session, a day -- must pass false and is priced at base rates.
//
// Negative counts are treated as zero, and CacheCreation1h is clamped to CacheCreation.
func Cost(r Rates, t Tokens, requestScoped bool) Estimate {
	t = t.clamped()
	var est Estimate
	set := r.RateSet
	if requestScoped {
		prompt := saturatingAdd(saturatingAdd(t.Input, t.CacheRead), t.CacheCreation)
		for _, b := range r.Bands {
			if prompt > b.AboveTokens {
				set = b.RateSet
				est.BandAboveTokens = b.AboveTokens
			}
		}
	}

	cacheRead := set.CacheRead
	if cacheRead <= 0 {
		cacheRead = set.Input
		est.CacheReadAtInputRate = t.CacheRead > 0
	}
	cacheWrite := set.CacheWrite
	if cacheWrite <= 0 {
		cacheWrite = set.Input
		est.CacheWriteAtInputRate = t.CacheCreation > 0
	}
	cacheWrite1h := set.CacheWrite1h
	if cacheWrite1h <= 0 {
		cacheWrite1h = cacheWrite
		est.CacheWrite1hAtWriteRate = t.CacheCreation1h > 0
	}
	output := t.Output
	if output == 0 && t.Reasoning > 0 {
		output = t.Reasoning
		est.ReasoningAtOutputRate = true
	}

	var sum lineItem
	sum.add(t.Input, set.Input)
	sum.add(t.CacheCreation-t.CacheCreation1h, cacheWrite)
	sum.add(t.CacheCreation1h, cacheWrite1h)
	sum.add(t.CacheRead, cacheRead)
	sum.add(output, set.Output)
	est.Cost, est.Saturated = sum.total()
	return est
}

func (t Tokens) clamped() Tokens {
	nonNeg := func(v int64) int64 {
		if v < 0 {
			return 0
		}
		return v
	}
	t.Input = nonNeg(t.Input)
	t.Output = nonNeg(t.Output)
	t.CacheRead = nonNeg(t.CacheRead)
	t.CacheCreation = nonNeg(t.CacheCreation)
	t.CacheCreation1h = nonNeg(t.CacheCreation1h)
	t.Reasoning = nonNeg(t.Reasoning)
	if t.CacheCreation1h > t.CacheCreation {
		t.CacheCreation1h = t.CacheCreation
	}
	return t
}

func saturatingAdd(a, b int64) int64 {
	if a > 0 && b > (1<<63-1)-a {
		return 1<<63 - 1
	}
	return a + b
}
