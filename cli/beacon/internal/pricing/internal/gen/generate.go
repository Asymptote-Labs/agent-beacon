package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

func sourceInfo(commit, fetched string) pricing.SourceInfo {
	return pricing.SourceInfo{
		Name:      "LiteLLM " + upstreamPath,
		License:   "MIT",
		URL:       upstreamURL,
		Commit:    commit,
		FetchedAt: fetched,
	}
}

// firstParty are the upstream providers whose entries are kept: the model vendors themselves
// and the clouds coding agents reach Claude and Gemini through. Resellers, inference hosts and
// subscription products (github_copilot, chatgpt) are not list prices for the call a runtime
// reports, and are left out.
var firstParty = map[string]bool{
	"anthropic":                  true,
	"openai":                     true,
	"gemini":                     true,
	"vertex_ai":                  true,
	"vertex_ai-language-models":  true,
	"vertex_ai-anthropic_models": true,
	"bedrock":                    true,
	"bedrock_converse":           true,
	"deepseek":                   true,
	"xai":                        true,
	"mistral":                    true,
	"moonshot":                   true,
	"dashscope":                  true,
	"zai":                        true,
	"minimax":                    true,
}

// openrouter entries are kept only for models no first-party entry prices -- usually ones the
// vendor has retired from its own list but runtimes still report -- and only from the same
// model vendors firstParty covers, so the catalog does not grow into a directory of every
// model OpenRouter hosts.
const openrouter = "openrouter"

var openrouterVendors = map[string]bool{
	"anthropic": true, "openai": true, "google": true, "deepseek": true, "x-ai": true,
	"mistralai": true, "moonshotai": true, "qwen": true, "z-ai": true, "minimax": true,
}

// excludedNameParts drop entries that are chat-mode in the upstream list but are not what a
// coding agent's token usage is billed as: audio, realtime, search, image and similar products
// priced by other units.
var excludedNameParts = []string{
	"audio", "realtime", "search", "tts", "image", "transcribe", "embedding", "lyria",
	"robotics", "computer-use", "deep-research", "container",
}

var bedrockAnthropicKey = regexp.MustCompile(`^((us-gov|global|apac|us|eu|au|jp)\.)?anthropic\.claude-`)

// bandKey matches the long-context fields, e.g. input_cost_per_token_above_200k_tokens or
// cache_creation_input_token_cost_above_1hr_above_200k_tokens. Variants with a further tier
// suffix (_batches, _priority, _flex) do not match, by the anchor.
var bandKey = regexp.MustCompile(`^(input_cost_per_token|output_cost_per_token|cache_read_input_token_cost|cache_creation_input_token_cost|cache_creation_input_token_cost_above_1hr)_above_(\d+)k_tokens$`)

func keep(key, provider, mode string) bool {
	if mode != "chat" && mode != "responses" {
		return false
	}
	if !firstParty[provider] && provider != openrouter {
		return false
	}
	lower := strings.ToLower(key)
	if strings.HasPrefix(lower, "ft:") {
		return false
	}
	for _, part := range excludedNameParts {
		if strings.Contains(lower, part) {
			return false
		}
	}
	switch {
	case strings.HasPrefix(provider, "bedrock"):
		// Only Claude, and only the model ids and inference profiles a runtime reports: the
		// bedrock/<region>/... and commitment-term paths are regional or provisioned prices.
		return bedrockAnthropicKey.MatchString(lower)
	case strings.HasPrefix(provider, "vertex_ai"):
		return strings.Contains(lower, "claude") || strings.Contains(lower, "gemini")
	case provider == openrouter:
		// :free, :batch, :exacto and ~latest aliases are routing products, not list prices.
		parts := strings.Split(lower, "/")
		return len(parts) == 3 && openrouterVendors[parts[1]] && !strings.ContainsAny(lower, ":~")
	}
	return true
}

// Rounding records a rate that was not an exact number of microdollars per million tokens.
type Rounding struct {
	Key, Field, Value string
	Rounded           int64
}

type report struct {
	Kept       int
	ByProvider map[string]int
	Rounded    []Rounding
}

func (r report) print(w io.Writer) {
	providers := make([]string, 0, len(r.ByProvider))
	for p := range r.ByProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		fmt.Fprintf(w, "  %-28s %d\n", p, r.ByProvider[p])
	}
	if len(r.Rounded) == 0 {
		fmt.Fprintln(w, "every kept rate converted exactly to integer microdollars per million tokens")
		return
	}
	fmt.Fprintf(w, "%d rate(s) rounded to the nearest microdollar per million tokens:\n", len(r.Rounded))
	for _, x := range r.Rounded {
		fmt.Fprintf(w, "  %s %s = %s -> %d\n", x.Key, x.Field, x.Value, x.Rounded)
	}
}

// generate turns the upstream list into catalog.json bytes.
func generate(raw []byte, source pricing.SourceInfo, generatedAt string) ([]byte, report, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var upstream map[string]map[string]any
	if err := dec.Decode(&upstream); err != nil {
		return nil, report{}, fmt.Errorf("parse upstream list: %w", err)
	}
	rep := report{ByProvider: map[string]int{}}

	keys := make([]string, 0, len(upstream))
	for k := range upstream {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	models := map[string]pricing.Rates{}
	var routed []string
	for _, key := range keys {
		entry := upstream[key]
		provider, _ := entry["litellm_provider"].(string)
		mode, _ := entry["mode"].(string)
		if key == "sample_spec" || !keep(key, provider, mode) {
			continue
		}
		rates, ok := convert(key, provider, entry, &rep)
		if !ok {
			continue
		}
		if provider == openrouter {
			routed = append(routed, key)
			continue
		}
		models[key] = rates
	}

	// An openrouter entry is added only when the first-party catalog cannot price its model
	// under the same lookup a report will use.
	firstPartyOnly, err := pricing.LoadCatalog(encode(source, generatedAt, models))
	if err != nil {
		return nil, rep, fmt.Errorf("first-party catalog: %w", err)
	}
	for _, key := range routed {
		res, ok := firstPartyOnly.Lookup(asymptoteobserve.NormalizeModelName(key))
		if ok || len(res.Candidates) > 0 {
			continue
		}
		rates, _ := convert(key, openrouter, upstream[key], &report{ByProvider: map[string]int{}})
		models[key] = rates
	}

	// Rounding is reported only for entries that made it in.
	kept := rep.Rounded[:0]
	for _, r := range rep.Rounded {
		if _, ok := models[r.Key]; ok {
			kept = append(kept, r)
		}
	}
	rep.Rounded = kept
	for _, r := range models {
		rep.ByProvider[r.Provider]++
	}
	rep.Kept = len(models)

	out := encode(source, generatedAt, models)
	if _, err := pricing.LoadCatalog(out); err != nil {
		return nil, rep, fmt.Errorf("generated catalog does not load: %w", err)
	}
	return out, rep, nil
}

// convert reads one upstream entry's standard-tier rates. It reports false for an entry with no
// positive input and output price, which is not something an estimate can use.
func convert(key, provider string, entry map[string]any, rep *report) (pricing.Rates, bool) {
	rate := func(field string) int64 {
		n, ok := entry[field].(json.Number)
		if !ok {
			return 0
		}
		v, rounded, exact, ok := perMillionMicrodollars(n)
		if !ok {
			return 0
		}
		if !exact {
			rep.Rounded = append(rep.Rounded, Rounding{Key: key, Field: field, Value: n.String(), Rounded: rounded})
		}
		return v
	}
	base := pricing.RateSet{
		Input:        rate("input_cost_per_token"),
		Output:       rate("output_cost_per_token"),
		CacheRead:    rate("cache_read_input_token_cost"),
		CacheWrite:   rate("cache_creation_input_token_cost"),
		CacheWrite1h: rate("cache_creation_input_token_cost_above_1hr"),
	}
	if base.CacheRead == 0 {
		base.CacheRead = rate("input_cost_per_token_cache_hit")
	}
	if base.Input <= 0 || base.Output <= 0 {
		return pricing.Rates{}, false
	}

	bands := map[int64]*pricing.RateSet{}
	var fields []string
	for field := range entry {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		m := bandKey.FindStringSubmatch(field)
		if m == nil {
			continue
		}
		v := rate(field)
		if v <= 0 {
			continue
		}
		thousands, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || thousands <= 0 {
			continue
		}
		above := thousands * 1000
		b := bands[above]
		if b == nil {
			b = &pricing.RateSet{}
			bands[above] = b
		}
		switch m[1] {
		case "input_cost_per_token":
			b.Input = v
		case "output_cost_per_token":
			b.Output = v
		case "cache_read_input_token_cost":
			b.CacheRead = v
		case "cache_creation_input_token_cost":
			b.CacheWrite = v
		case "cache_creation_input_token_cost_above_1hr":
			b.CacheWrite1h = v
		}
	}

	rates := pricing.Rates{Provider: provider, RateSet: base}
	thresholds := make([]int64, 0, len(bands))
	for above := range bands {
		thresholds = append(thresholds, above)
	}
	sort.Slice(thresholds, func(i, j int) bool { return thresholds[i] < thresholds[j] })
	for _, above := range thresholds {
		// A field the band does not restate costs what it costs below the threshold.
		set := *bands[above]
		if set.Input == 0 {
			set.Input = base.Input
		}
		if set.Output == 0 {
			set.Output = base.Output
		}
		if set.CacheRead == 0 {
			set.CacheRead = base.CacheRead
		}
		if set.CacheWrite == 0 {
			set.CacheWrite = base.CacheWrite
		}
		if set.CacheWrite1h == 0 {
			set.CacheWrite1h = base.CacheWrite1h
		}
		if set == base {
			continue
		}
		rates.Bands = append(rates.Bands, pricing.Band{AboveTokens: above, RateSet: set})
	}
	return rates, true
}

var perTokenToPerMillionMicro = new(big.Rat).SetInt64(1_000_000 * 1_000_000)

// perMillionMicrodollars converts a dollars-per-token decimal to integer microdollars per
// million tokens, exactly: the decimal is read as a rational, not a float, so 3e-06 is 3_000_000
// and not 2_999_999.9999. A value that is not an exact integer is rounded half up and reported.
func perMillionMicrodollars(n json.Number) (value, rounded int64, exact, ok bool) {
	r, good := new(big.Rat).SetString(n.String())
	if !good || r.Sign() < 0 {
		return 0, 0, false, false
	}
	r.Mul(r, perTokenToPerMillionMicro)
	if r.IsInt() {
		if !r.Num().IsInt64() {
			return 0, 0, false, false
		}
		return r.Num().Int64(), 0, true, true
	}
	// floor((2*num + den) / (2*den)) is num/den rounded half up.
	num := new(big.Int).Mul(r.Num(), big.NewInt(2))
	num.Add(num, r.Denom())
	den := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	num.Div(num, den)
	if !num.IsInt64() {
		return 0, 0, false, false
	}
	return num.Int64(), num.Int64(), false, true
}

// encode writes the catalog with a fixed field order and one model per line.
func encode(source pricing.SourceInfo, generatedAt string, models map[string]pricing.Rates) []byte {
	var b bytes.Buffer
	sourceJSON, _ := json.Marshal(source)
	fmt.Fprintf(&b, "{\n  \"schema_version\": %d,\n", pricing.SchemaVersion)
	fmt.Fprintf(&b, "  \"source\": %s,\n", sourceJSON)
	fmt.Fprintf(&b, "  \"generated_at\": %s,\n", mustJSON(generatedAt))
	fmt.Fprintf(&b, "  \"unit\": %s,\n", mustJSON(pricing.RateUnit))
	b.WriteString("  \"models\": {\n")
	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		entry, _ := json.Marshal(models[k])
		fmt.Fprintf(&b, "    %s: %s", mustJSON(k), entry)
		if i < len(keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("  }\n}\n")
	return b.Bytes()
}

func mustJSON(s string) []byte {
	out, _ := json.Marshal(s)
	return out
}
