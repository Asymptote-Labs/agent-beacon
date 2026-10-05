package tokens

import (
	"sort"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pricing"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// Cost provenance.
//
// A report carries a runtime-reported cost (cost_usd), a list-price estimate
// (estimated_cost_usd) and an effective cost made of one or the other. The estimate exists so
// that runtimes which never report a price -- Codex, DeepSeek Harness, OpenClaw, Factory, Gemini
// CLI and Copilot before it reported AI credits among them -- do not read as free. It is
// report-time arithmetic over the final per-event deltas and is never written back into an
// event: gen_ai.usage.cost_usd stays what the runtime said.
//
// The effective cost follows one rule. Within one (endpoint, harness, session) scope, if any
// event reported a cost, the scope's cost is what the runtime reported and the estimate takes no
// part in it: a runtime's own figure knows its plan, its discounts and its routing, which a list
// price cannot. That is also what keeps Claude Code correct, whose cost arrives on a separate
// cost metric beside token events that carry none: the scope is reported, so its token events
// add nothing on top. A scope with no reported cost contributes its estimate. Events without a
// session have no scope and are judged one at a time.

// Values of Usage.CostSource.
const (
	CostSourceReported  = "reported"
	CostSourceEstimated = "estimated"
	CostSourceMixed     = "mixed"
)

// PricingTier names the price list an estimate is drawn from. Batch, flex and priority tiers,
// regional surcharges and subscription plans are not modelled: nothing in a usage record says a
// call used them.
const PricingTier = "standard"

// priceCatalog is the catalog estimates are priced from when Options.Pricer is nil; a variable
// so tests can substitute one.
var priceCatalog = pricing.Default

// PricingSummary is the report's account of its estimates.
type PricingSummary struct {
	Catalog PricingCatalog `json:"catalog"`
	// Overrides names the overrides file the estimate consulted, by path and digest, so an
	// estimate drawn from it can be traced to the exact bytes. Nil when there was none.
	Overrides *PricingOverrides `json:"overrides,omitempty"`
	Tier      string            `json:"tier"`
	// Models lists every model the estimate priced, sorted by model.
	Models []ModelPricing `json:"models,omitempty"`
	// Unpriced lists the models the estimate could not price, sorted by model. Their tokens are
	// in every token total and in no estimate.
	Unpriced []UnpricedModel `json:"unpriced,omitempty"`
}

// PricingCatalog is the provenance of the embedded price list.
type PricingCatalog struct {
	Name        string `json:"name"`
	License     string `json:"license"`
	URL         string `json:"url"`
	Commit      string `json:"commit,omitempty"`
	FetchedAt   string `json:"fetched_at"`
	GeneratedAt string `json:"generated_at"`
}

// PricingOverrides is the overrides file behind a report. Error is set when a file was asked
// for and could not be used; the estimate is then list price alone, and Models and Aliases are
// zero.
type PricingOverrides struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256,omitempty"`
	Models  int    `json:"models"`
	Aliases int    `json:"aliases"`
	Error   string `json:"error,omitempty"`
}

// RatesPerMTok is a rate set in US dollars per million tokens. A zero cache rate means the
// provider publishes none, and the estimate priced those tokens by the fallback the model's
// Fallbacks name.
type RatesPerMTok struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read,omitempty"`
	CacheWrite   float64 `json:"cache_write,omitempty"`
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`
}

// BandUse counts the requests a long-context band priced.
type BandUse struct {
	AboveTokens int64 `json:"above_tokens"`
	Requests    int   `json:"requests"`
}

// ModelPricing is how one reported model was priced.
type ModelPricing struct {
	// Model is the model as the report groups it; Key is the entry that priced it, and Source
	// says whether that is a catalog key ("catalog") or a row of the overrides file
	// ("override"). Alias is set when the model reached Key through an alias in the overrides
	// file.
	Model    string   `json:"model"`
	Key      string   `json:"key"`
	Source   string   `json:"source"`
	Alias    string   `json:"alias,omitempty"`
	Provider string   `json:"provider"`
	Match    string   `json:"match"`
	Stripped []string `json:"stripped,omitempty"`
	// Rates are the base rates. A request priced in a long-context band used that band's rates
	// instead, and is counted in BandsApplied.
	Rates RatesPerMTok `json:"rates_usd_per_mtok"`
	// Events is the number of priced usage events; RequestScopedEvents those known to be one
	// model request, the only ones a band can apply to.
	Events              int       `json:"events"`
	RequestScopedEvents int       `json:"request_scoped_events"`
	BandsApplied        []BandUse `json:"bands_applied,omitempty"`
	// Fallbacks names each missing-rate fallback that priced a non-zero count:
	// cache_read_at_input_rate, cache_write_at_input_rate, reasoning_at_output_rate.
	Fallbacks        []string `json:"fallbacks,omitempty"`
	EstimatedCostUSD float64  `json:"estimated_cost_usd"`

	estimated pricing.Microdollars
	bands     map[int64]int
	fallbacks map[string]bool
}

// UnpricedModel is a model the estimate could not price. Model is empty for usage that named no
// model at all; Candidates is set when several entries matched with different prices, and
// CandidatesSource says whether they are catalog keys or names in the overrides file.
type UnpricedModel struct {
	Model            string   `json:"model"`
	Events           int      `json:"events"`
	Tokens           int64    `json:"tokens"`
	Candidates       []string `json:"candidates,omitempty"`
	CandidatesSource string   `json:"candidates_source,omitempty"`
}

// settle derives the exported cost fields from the exact internal sums.
func (u *Usage) settle() {
	u.EstimatedCostUSD = u.estimated.USD()
	u.EffectiveCostUSD = u.effectiveReported + u.effectiveEstimated.USD()
	switch {
	case u.reportedEvents > 0 && u.estimatedEvents > 0:
		u.CostSource = CostSourceMixed
	case u.reportedEvents > 0:
		u.CostSource = CostSourceReported
	case u.estimatedEvents > 0:
		u.CostSource = CostSourceEstimated
	default:
		u.CostSource = ""
	}
}

// pricedTokens is the token count an estimate covers: the disjoint total, plus reasoning when
// it is the only output reported (pricing bills it as output then; otherwise it is inside output).
func pricedTokens(u Usage) int64 {
	n := u.TotalTokens()
	if u.OutputTokens == 0 {
		n += u.ReasoningOutputTokens
	}
	return n
}

// Harness names whose usage events requestScoped knows the granularity of.
var (
	piFamilyHarnesses = map[string]bool{"pi_cli": true, "omp": true, "prime_agent": true, "omo_senpi": true}
	claudeOTelHarness = map[string]bool{"claude_code": true, "claude_cowork": true}
)

// requestScoped reports whether an event's usage is exactly one model request, which is the
// only case a long-context band can be applied to (see pricing.Cost). It is decided per capture
// path from what that path is known to record, and defaults to false: pricing a sum of requests
// at base rates can understate a session that sent long prompts, while banding a sum would
// overstate every session that merely sent many short ones.
//
// Request-scoped:
//   - Claude Code and Claude Cowork claude_code.api_request log records (one per API request),
//     and Claude Code session-file usage, which is the usage block of one assistant API response.
//   - Codex session-file token_count records: last_token_usage is one completion, and the
//     fallback difference between consecutive cumulative totals spans the one completion
//     between them.
//   - Pi-family message_end usage (Pi, Oh My Pi, Prime Agent, Senpi) and their session-file
//     usage: one finalized assistant message is one model call.
//   - OpenCode usage: each assistant message is one LLM step, and its tokens are that step's.
//   - OpenClaw session-file usage, recorded per assistant message.
//   - DeepSeek Harness usage from one assistant message.
//
// Aggregate, priced at base rates: every metric datapoint (an interval sum, cumulative ones
// already differenced), Codex turn spans and token_usage_record turn totals, OpenClaw's
// llm_output hook (one report per turn, across the turn's tool loop), Cline task totals,
// Copilot shutdown deltas and assistant output counts, Hermes, Factory and fx cumulative or
// per-turn deltas, Cursor Admin API usage, and any OTLP span of unknown granularity.
func requestScoped(ue *usageEvent) bool {
	if ue.metricName != "" || ue.cumulative {
		return false
	}
	harness := asymptoteobserve.NormalizeHarnessName(ue.harness)
	poll := strings.EqualFold(ue.collectionMethod, schema.CollectionMethodPoll)
	switch {
	case claudeOTelHarness[harness]:
		if harness == "claude_code" && poll {
			return true
		}
		name := strings.ToLower(strings.TrimSpace(ue.name))
		return name == "claude_code.api_request" || name == "api_request"
	case harness == "codex_cli" || harness == "codex_desktop":
		return ue.codexSessionSource == "codex_session_token_count"
	case piFamilyHarnesses[harness]:
		// The hook path also attaches usage to a tool result when a build sends one there; that
		// one is not known to be a single request.
		return ue.action == "token.usage" || poll
	case harness == "opencode":
		return true
	case harness == "openclaw_gateway":
		return poll
	case harness == "deepseek_harness":
		return ue.tokenSource == "assistant_message"
	}
	return false
}

type costScopeKey struct {
	endpoint string
	harness  string
	session  string
}

func costScope(ue *usageEvent) (costScopeKey, bool) {
	session := strings.ToLower(strings.TrimSpace(ue.session))
	if session == "" {
		return costScopeKey{}, false
	}
	return costScopeKey{
		endpoint: strings.ToLower(strings.TrimSpace(ue.endpoint)),
		harness:  asymptoteobserve.NormalizeHarnessName(ue.harness),
		session:  session,
	}, true
}

type resolvedModel struct {
	res    pricing.Resolution
	priced bool
}

// priceUsageEvents sets every usage event's estimated and effective cost and returns the
// report's pricing summary. Context-only events are not spend and are skipped.
func priceUsageEvents(events []*usageEvent, pricer *pricing.Pricer) *PricingSummary {
	catalog := pricer.Catalog()
	source := catalog.Source()
	summary := &PricingSummary{
		Catalog: PricingCatalog{
			Name:        source.Name,
			License:     source.License,
			URL:         source.URL,
			Commit:      source.Commit,
			FetchedAt:   source.FetchedAt,
			GeneratedAt: catalog.GeneratedAt(),
		},
		Tier:      PricingTier,
		Overrides: overridesSummary(pricer),
	}

	reportedScopes := map[costScopeKey]bool{}
	for _, ue := range events {
		if ue.contextOnly || ue.usage.CostUSD <= 0 {
			continue
		}
		if key, ok := costScope(ue); ok {
			reportedScopes[key] = true
		}
	}

	lookups := map[string]resolvedModel{}
	models := map[string]*ModelPricing{}
	unpriced := map[string]*UnpricedModel{}
	for _, ue := range events {
		if ue.contextOnly {
			continue
		}
		u := &ue.usage
		tokens := pricedTokens(*u)
		priced := false
		if tokens > 0 {
			resolved, seen := lookups[ue.model]
			if !seen {
				resolved.res, resolved.priced = pricer.Lookup(ue.model)
				lookups[ue.model] = resolved
			}
			if resolved.priced {
				scoped := requestScoped(ue)
				est := pricing.Cost(resolved.res.Rates, pricing.Tokens{
					Input:         u.InputTokens,
					Output:        u.OutputTokens,
					CacheRead:     u.CacheReadInputTokens,
					CacheCreation: u.CacheCreationInputTokens,
					// gen_ai.usage does not carry the one-hour cache-write split yet, so every
					// write is priced at the five-minute rate.
					CacheCreation1h: 0,
					Reasoning:       u.ReasoningOutputTokens,
				}, scoped)
				// A saturated estimate means the counts are corrupt; it is not a price.
				if !est.Saturated {
					priced = true
					u.estimated = est.Cost
					models[ue.model] = recordModelPricing(models[ue.model], ue.model, resolved.res, scoped, est)
				}
			}
			if !priced {
				u.UnpricedEvents = 1
				u.UnpricedTokens = tokens
				entry := unpriced[ue.model]
				if entry == nil {
					entry = &UnpricedModel{Model: ue.model, Candidates: resolved.res.Candidates}
					if len(entry.Candidates) > 0 {
						entry.CandidatesSource = resolved.res.Source
					}
					unpriced[ue.model] = entry
				}
				entry.Events++
				entry.Tokens += tokens
			}
		}

		reported := u.CostUSD > 0
		if key, ok := costScope(ue); ok {
			reported = reportedScopes[key]
		}
		switch {
		case reported:
			u.effectiveReported = u.CostUSD
			u.reportedEvents = 1
		case priced:
			u.effectiveEstimated = u.estimated
			u.estimatedEvents = 1
		}
		u.settle()
	}

	for _, model := range sortedKeys(models) {
		m := models[model]
		m.EstimatedCostUSD = m.estimated.USD()
		for _, above := range sortedInt64Keys(m.bands) {
			m.BandsApplied = append(m.BandsApplied, BandUse{AboveTokens: above, Requests: m.bands[above]})
		}
		for _, name := range sortedKeys(m.fallbacks) {
			m.Fallbacks = append(m.Fallbacks, name)
		}
		summary.Models = append(summary.Models, *m)
	}
	for _, model := range sortedKeys(unpriced) {
		summary.Unpriced = append(summary.Unpriced, *unpriced[model])
	}
	return summary
}

func overridesSummary(pricer *pricing.Pricer) *PricingOverrides {
	if o := pricer.Overrides(); o != nil {
		return &PricingOverrides{Path: o.Path(), SHA256: o.SHA256(), Models: len(o.Models()), Aliases: len(o.Aliases())}
	}
	if path, err := pricer.OverridesError(); err != nil {
		return &PricingOverrides{Path: path, Error: err.Error()}
	}
	return nil
}

func recordModelPricing(m *ModelPricing, model string, res pricing.Resolution, scoped bool, est pricing.Estimate) *ModelPricing {
	if m == nil {
		m = &ModelPricing{
			Model:     model,
			Key:       res.Key,
			Source:    res.Source,
			Alias:     res.Alias,
			Provider:  res.Rates.Provider,
			Match:     string(res.Match),
			Stripped:  append([]string(nil), res.Stripped...),
			Rates:     RatesUSDPerMTok(res.Rates.RateSet),
			bands:     map[int64]int{},
			fallbacks: map[string]bool{},
		}
	}
	m.Events++
	if scoped {
		m.RequestScopedEvents++
	}
	if est.BandAboveTokens > 0 {
		m.bands[est.BandAboveTokens]++
	}
	if est.CacheReadAtInputRate {
		m.fallbacks["cache_read_at_input_rate"] = true
	}
	if est.CacheWriteAtInputRate {
		m.fallbacks["cache_write_at_input_rate"] = true
	}
	if est.ReasoningAtOutputRate {
		m.fallbacks["reasoning_at_output_rate"] = true
	}
	m.estimated += est.Cost
	return m
}

// RatesUSDPerMTok converts a rate set to US dollars per million tokens for display.
func RatesUSDPerMTok(set pricing.RateSet) RatesPerMTok {
	usd := func(v int64) float64 { return pricing.Microdollars(v).USD() }
	return RatesPerMTok{
		Input:        usd(set.Input),
		Output:       usd(set.Output),
		CacheRead:    usd(set.CacheRead),
		CacheWrite:   usd(set.CacheWrite),
		CacheWrite1h: usd(set.CacheWrite1h),
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedInt64Keys(m map[int64]int) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
